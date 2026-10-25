package server

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/docs"
)

// These tests run against whatever docs/*.md currently says (TestMain
// points lib/docs at ../docs), so they name no specific heading: they
// check properties that must hold for every page.

var (
	idAttr   = regexp.MustCompile(`\sid="([^"]*)"`)
	hrefAttr = regexp.MustCompile(`\shref="([^"]*)"`)
)

// renderedIDs fetches /ui/docs/<key> and returns the id attributes in it.
func renderedIDs(t *testing.T, r http.Handler, key string) (string, map[string]int) {
	t.Helper()
	w := getUI(r, "/ui/docs/"+key)
	require.Equal(t, http.StatusOK, w.Code, "page %s", key)
	body := w.Body.String()
	ids := map[string]int{}
	for _, m := range idAttr.FindAllStringSubmatch(body, -1) {
		ids[html.UnescapeString(m[1])]++
	}
	return body, ids
}

// Every page renders, and every section the search index knows about has
// a heading with the same anchor -- so a search hit's #fragment lands.
func TestUIDocs_EveryPageRendersWithIndexAnchors(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	for _, key := range docs.Keys() {
		body, ids := renderedIDs(t, r, key)
		// goldmark drops raw HTML, and so does GitHub: a bare <word> in
		// prose vanishes from both. Write it as &lt;word&gt; or in backticks.
		assert.NotContains(t, body, "raw HTML omitted", "page %s has raw HTML in its markdown", key)
		secs, err := docs.Sections(key)
		require.NoError(t, err)
		require.NotEmpty(t, secs)
		for _, sec := range secs {
			if sec.ID == "#" {
				continue
			}
			assert.Equal(t, 1, ids[strings.TrimPrefix(sec.ID, "#")],
				"page %s: section %q (%s) should have exactly one matching id", key, sec.Heading, sec.ID)
		}
	}
}

// Every link from one doc page to another (or within one) resolves to a
// served page and an anchor that page renders.
func TestUIDocs_InternalLinksResolve(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	bodies := map[string]string{}
	ids := map[string]map[string]int{}
	for _, key := range docs.Keys() {
		bodies[key], ids[key] = renderedIDs(t, r, key)
	}
	checked := 0
	for key, body := range bodies {
		article := body[strings.Index(body, `<article class="doc-body">`):]
		for _, m := range hrefAttr.FindAllStringSubmatch(article, -1) {
			href := html.UnescapeString(m[1])
			target, frag := key, ""
			switch {
			case strings.HasPrefix(href, "#"):
				frag = href[1:]
			case strings.HasPrefix(href, "/ui/docs/"):
				p, f, _ := strings.Cut(strings.TrimPrefix(href, "/ui/docs/"), "#")
				target, frag = p, f
				_, ok := ids[target]
				if !assert.True(t, ok, "page %s links to unknown page %q", key, href) {
					continue
				}
			default:
				assert.False(t, strings.HasSuffix(strings.SplitN(href, "#", 2)[0], ".md") && !strings.Contains(href, "://"),
					"page %s has an unrewritten relative link %q", key, href)
				continue
			}
			checked++
			if frag != "" {
				frag, _ = url.PathUnescape(frag)
				assert.Equal(t, 1, ids[target][frag], "page %s links to %q, which has no matching anchor", key, href)
			}
		}
	}
	assert.Positive(t, checked, "expected the docs to link to each other at least once")
}

func TestUIDocs_LinkTarget(t *testing.T) {
	cases := map[string]string{
		"api.md":                  "/ui/docs/api",
		"task_flow.md#task-state": "/ui/docs/flow#task-state",
		"README.md":               "/ui/docs/overview",
		"../README.md":            docsGitHubBase + "README.md",
		"examples/tailscale.md":   docsGitHubBase + "docs/examples/tailscale.md",
		"mcp.md#tools":            docsGitHubBase + "docs/mcp.md#tools",
		"#errors":                 "#errors",
		"https://example.com/x":   "https://example.com/x",
		"/ui/workers":             "/ui/workers",
	}
	for in, want := range cases {
		assert.Equal(t, want, docLinkTarget(in), in)
	}
}

// Searching for a section's heading returns a link to that section, both
// from the live-results partial and the no-JS ?q= form submission.
func TestUIDocs_SearchLinksToSection(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	var target docs.Section
	for _, key := range docs.Keys() {
		secs, err := docs.Sections(key)
		require.NoError(t, err)
		for _, sec := range secs {
			if sec.Level == 2 && len(strings.Fields(sec.Heading)) >= 2 {
				target = sec
				break
			}
		}
		if target.ID != "" {
			break
		}
	}
	require.NotEmpty(t, target.ID, "no multi-word ## heading anywhere in docs/")

	// Search for the exact heading the way a user would, and check the
	// section's own link is among the hits.
	want := `href="/ui/docs/` + target.Page + target.ID + `"`
	q := url.QueryEscape(target.Heading)
	for _, path := range []string{"/ui/partials/docs-search?q=" + q, "/ui/docs?q=" + q} {
		w := getUI(r, path)
		require.Equal(t, http.StatusOK, w.Code, path)
		assert.Contains(t, w.Body.String(), want, path)
	}

	w := getUI(r, "/ui/partials/docs-search?q=zzqxnotaword")
	assert.Contains(t, w.Body.String(), "No sections match")
}

func TestUIDocs_IndexListsEveryPage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	w := getUI(s.GetRouter(), "/ui/docs")
	require.Equal(t, http.StatusOK, w.Code)
	for _, key := range docs.Keys() {
		assert.Contains(t, w.Body.String(), `href="/ui/docs/`+key+`"`)
	}
}

func TestUIDocs_UnknownPage404(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	w := getUI(s.GetRouter(), "/ui/docs/no-such-page")
	assert.Equal(t, http.StatusNotFound, w.Code)
}
