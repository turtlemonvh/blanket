package server

// The docs pages of the web UI (turtlemonvh/blanket#234): the markdown
// under docs/ that ships inside the binary, rendered to HTML and searched
// with the same lib/docs index the blanket_docs MCP tool uses, so a user
// on a network without GitHub access can still read the docs for the
// exact version they are running.

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/docs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// docsGitHubBase is where links to files that aren't served pages go
// (the top-level README, docs/examples/, pages outside lib/docs' map).
const docsGitHubBase = "https://github.com/turtlemonvh/blanket/blob/master/"

// docsSearchLimit is how many sections a UI search lists.
const docsSearchLimit = 10

// Raw HTML in the markdown is dropped (goldmark's default): the docs are
// trusted, but nothing in them needs it, and escaping is the safe default.
//
// Tables only, not the full extension.GFM set: the docs use tables
// heavily and no strikethrough or task lists, and the rest of GFM
// (mostly linkify) cost another 70 KB of binary (#179).
var docsMarkdown = goldmark.New(goldmark.WithExtensions(extension.Table))

// DocPageView is one entry on the docs index.
type DocPageView struct {
	Key, Title string
}

// DocHitView is one search result, or (URL and Heading only) one entry
// in a page's "On this page" list.
type DocHitView struct {
	URL, PageTitle, Heading, Snippet string
}

// plainHeading drops the code-span backticks from a raw heading for
// display as text ("`GET /task/`" reads as "GET /task/").
func plainHeading(h string) string { return strings.ReplaceAll(h, "`", "") }

// docTitle is a page's "#" heading, or its key if it has none.
func docTitle(key string, secs []docs.Section) string {
	for _, s := range secs {
		if s.Level == 1 && s.Heading != "" {
			return plainHeading(s.Heading)
		}
	}
	return key
}

// renderDoc renders a page's markdown to HTML. Headings get the anchors
// lib/docs gives the same sections, so a search hit's /ui/docs/:page#id
// lands on its heading; links between doc pages are rewritten to UI URLs.
func renderDoc(src []byte, secs []docs.Section) (template.HTML, error) {
	doc := docsMarkdown.Parser().Parse(text.NewReader(src))

	// lib/docs indexes "#".."###" headings in document order; walk the
	// same headings in the same order and take each one's ID from its
	// section. Anything that doesn't line up (setext headings, "####")
	// falls back to the same anchor rule, deduplicated against the rest.
	var heads []*ast.Heading
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			heads = append(heads, n)
		case *ast.Link:
			n.Destination = []byte(docLinkTarget(string(n.Destination)))
		}
		return ast.WalkContinue, nil
	})
	used := map[string]bool{}
	ids := make([]string, len(heads))
	next := 0
	for next < len(secs) && secs[next].ID == "#" {
		next++ // the untitled text-before-any-heading section
	}
	for i, h := range heads {
		if h.Level > 3 || next >= len(secs) {
			continue
		}
		if s := secs[next]; s.Level == h.Level && s.Heading == headingSource(h, src) {
			ids[i] = strings.TrimPrefix(s.ID, "#")
			used[ids[i]] = true
			next++
		}
	}
	for i, h := range heads {
		if ids[i] == "" {
			base := docs.Anchor(headingSource(h, src))
			id := base
			for n := 1; used[id]; n++ {
				id = fmt.Sprintf("%s-%d", base, n)
			}
			ids[i] = id
			used[id] = true
		}
		h.SetAttributeString("id", []byte(ids[i]))
	}

	var buf bytes.Buffer
	if err := docsMarkdown.Renderer().Render(&buf, src, doc); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// headingSource is a heading's raw markdown text -- what lib/docs builds
// its anchors from, e.g. "`GET /task/`" with its backticks.
func headingSource(h *ast.Heading, src []byte) string {
	var b strings.Builder
	for i := 0; i < h.Lines().Len(); i++ {
		seg := h.Lines().At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimSpace(b.String())
}

// docLinkTarget rewrites a link found in docs/*.md. A relative link to a
// page in lib/docs' map becomes its /ui/docs URL (fragment kept); any
// other relative link points at the file on GitHub, since the binary
// doesn't ship it. Absolute URLs and same-page "#x" links are unchanged.
func docLinkTarget(dest string) string {
	if dest == "" || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "/") ||
		strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:") {
		return dest
	}
	file, frag, _ := strings.Cut(dest, "#")
	if frag != "" {
		frag = "#" + frag
	}
	if key, ok := docs.KeyForFile(file); ok {
		return "/ui/docs/" + key + frag
	}
	return docsGitHubBase + path.Join("docs", file) + frag
}

// docsSearch runs q against the shared index and projects the hits.
func docsSearch(q string) ([]DocHitView, error) {
	res, err := docs.Search(q, docsSearchLimit)
	if err != nil {
		return nil, err
	}
	hits := make([]DocHitView, 0, len(res))
	for _, r := range res {
		secs, _ := docs.Sections(r.Page)
		url := "/ui/docs/" + r.Page
		if r.Level > 1 {
			url += r.ID
		}
		title := docTitle(r.Page, secs)
		heading := plainHeading(r.Heading)
		if heading == "" {
			heading = title
		}
		hits = append(hits, DocHitView{URL: url, PageTitle: title, Heading: heading, Snippet: r.Snippet})
	}
	return hits, nil
}

// uiDocsPage lists the doc pages, plus search results for ?q= (the search
// box works without htmx too: it is a plain GET form).
func (s *ServerConfig) uiDocsPage(c *gin.Context) {
	var pages []DocPageView
	for _, key := range docs.Keys() {
		secs, err := docs.Sections(key)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		pages = append(pages, DocPageView{Key: key, Title: docTitle(key, secs)})
	}
	// The docs' own index page first, then the rest by title.
	sort.Slice(pages, func(i, j int) bool {
		if (pages[i].Key == "overview") != (pages[j].Key == "overview") {
			return pages[i].Key == "overview"
		}
		return pages[i].Title < pages[j].Title
	})

	q := strings.TrimSpace(c.Query("q"))
	var hits []DocHitView
	if q != "" {
		var err error
		if hits, err = docsSearch(q); err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
	}
	t := mustParseUIPage("docs", "ui/templates/docs.html", "ui/templates/docs_results.html")
	s.renderUI(c, t, gin.H{"Title": "Docs", "Pages": pages, "Query": q, "Hits": hits})
}

// uiDocsSearchPartial is the search box's live results (?q=).
func (s *ServerConfig) uiDocsSearchPartial(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	var hits []DocHitView
	if q != "" {
		var err error
		if hits, err = docsSearch(q); err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
	}
	t := mustParsePartial("docs-results", "docs_results.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "docs-results", gin.H{"Query": q, "Hits": hits}); err != nil {
		log.WithField("err", err).Warn("ui: render docs results")
	}
}

// uiDocPage renders one doc page.
func (s *ServerConfig) uiDocPage(c *gin.Context) {
	key := c.Param("page")
	secs, err := docs.Sections(key)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return
	}
	src, err := docs.Page(key)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	body, err := renderDoc([]byte(src), secs)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	// "On this page": the "##" sections, which is where the long pages
	// (api, upgrade) are worth jumping around in.
	var toc []DocHitView
	for _, sec := range secs {
		if sec.Level == 2 {
			toc = append(toc, DocHitView{URL: sec.ID, Heading: plainHeading(sec.Heading)})
		}
	}
	t := mustParseUIPage("doc-page", "ui/templates/doc_page.html")
	s.renderUI(c, t, gin.H{
		"Title": docTitle(key, secs),
		"Key":   key,
		"Body":  body,
		"TOC":   toc,
	})
}
