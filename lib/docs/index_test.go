package docs

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestSearch_RealQuestions pins a handful of questions an agent actually
// asks to the section that answers them. If ranking changes, these are
// what should keep passing.
func TestSearch_RealQuestions(t *testing.T) {
	cases := []struct {
		query, page, idPart string
	}{
		{"how do I cancel a running task", "api", "#cancel-a-task-put-taskidcancel"},
		{"how do I cancel a running task", "flow", "#cancel-a-running-task-forcetrue"},
		{"worker tags superset", "tags", "#how-tags-match-workers"},
		{"malformed id 400", "api", "#errors"},
		{"how do I start a worker", "", "worker"},
		{"task type required env vars", "schema", ""},
		{"rolling upgrade drain", "upgrade", ""},
	}
	for _, c := range cases {
		res, err := Search(c.query, 5)
		if err != nil {
			t.Fatalf("Search(%q): %v", c.query, err)
		}
		if len(res) == 0 {
			t.Errorf("Search(%q) returned nothing", c.query)
			continue
		}
		var got []string
		found := false
		for _, r := range res {
			got = append(got, r.Page+r.ID)
			if (c.page == "" || r.Page == c.page) && strings.Contains(r.ID, c.idPart) {
				found = true
			}
		}
		if !found {
			t.Errorf("Search(%q): want page %q id containing %q in top 5, got %v", c.query, c.page, c.idPart, got)
		}
		if top := res[0]; top.Snippet == "" || len(top.Snippet) > 210 {
			t.Errorf("Search(%q): snippet should be ~200 chars, got %d: %q", c.query, len(top.Snippet), top.Snippet)
		}
	}
}

func TestSearch_NoMatch(t *testing.T) {
	res, err := Search("zzqxyzzy", 5)
	if err != nil || len(res) != 0 {
		t.Errorf("want no results and no error, got %v, %v", res, err)
	}
}

func TestRead_SmallPageIsWhole(t *testing.T) {
	raw, _ := Page("authoring")
	v, err := Read("authoring", "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Full != raw || v.Text() != raw {
		t.Errorf("authoring (%d chars) should come back whole", len(raw))
	}
}

func TestRead_LargePageIsTOC(t *testing.T) {
	raw, _ := Page("api")
	if len(raw) <= MaxChars {
		t.Skipf("api.md is only %d chars; the test needs a page over %d", len(raw), MaxChars)
	}
	v, err := Read("api", "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Full != "" || len(v.TOC) == 0 {
		t.Fatalf("api.md should come back as a TOC, got Full=%d chars, TOC=%d", len(v.Full), len(v.TOC))
	}
	text := v.Text()
	if len(text) > MaxChars {
		t.Errorf("TOC itself is %d chars, over the %d cap", len(text), MaxChars)
	}
	if !strings.Contains(text, "#errors") {
		t.Errorf("api TOC should list #errors:\n%s", text)
	}
}

func TestRead_Section(t *testing.T) {
	for _, id := range []string{"#errors", "errors"} {
		v, err := Read("api", id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(v.Full, "## Errors") {
			t.Errorf("Read(api, %q) = %.60q, want the Errors section", id, v.Full)
		}
	}
	if _, err := Read("api", "#no-such-section"); err == nil {
		t.Error("want an error for an unknown section")
	}
	if _, err := Read("nope", ""); err == nil || !strings.Contains(err.Error(), "overview") {
		t.Errorf("want an error listing valid pages, got %v", err)
	}
}

// Every section of every real page is readable: whole within the cap, or
// as a TOC pointing at subsections that are.
func TestRead_EverySectionReachable(t *testing.T) {
	idx, err := defaultIndex()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d sections, %d distinct terms", len(idx.sections), len(idx.df))
	for _, s := range idx.sections {
		v, err := idx.Read(s.Page, s.ID)
		if err != nil {
			t.Errorf("Read(%s, %s): %v", s.Page, s.ID, err)
			continue
		}
		if v.Full == "" && len(v.TOC) == 0 {
			t.Errorf("Read(%s, %s) returned neither text nor TOC", s.Page, s.ID)
		}
		if len(v.Full) > MaxChars {
			t.Logf("note: %s%s returned whole at %d chars (no subsections to point at)", s.Page, s.ID, len(v.Full))
		}
	}
}

func TestOversizedSectionGivesSubsectionTOC(t *testing.T) {
	big := strings.Repeat("filler text here. ", MaxChars/18+10)
	md := "# T\n\nintro\n\n## Big\n\nlead-in\n\n### One\n\n" + big + "\n### Two\n\nshort\n\n## Small\n\nok\n"
	idx, err := BuildIndex(fstest.MapFS{"README.md": {Data: []byte(md)}, "authoring_task_types.md": {}, "task_type_definitions.md": {}, "tag_ontology.md": {}, "usage.md": {}, "api.md": {}, "task_flow.md": {}, "upgrade.md": {}, "install.md": {}, "offline_install.md": {}, "autostart.md": {}, "mcp.md": {}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := idx.Read("overview", "#big")
	if err != nil {
		t.Fatal(err)
	}
	if v.Full != "" || len(v.TOC) != 2 || v.TOC[0].ID != "#one" || !strings.Contains(v.Intro, "lead-in") {
		t.Errorf("want TOC [#one #two] with the lead-in, got Full=%d TOC=%v Intro=%q", len(v.Full), v.TOC, v.Intro)
	}
	if v, _ := idx.Read("overview", "#two"); !strings.Contains(v.Full, "short") {
		t.Errorf("#two should come back whole, got %q", v.Full)
	}
}

func TestAnchor(t *testing.T) {
	for in, want := range map[string]string{
		"Errors":                          "errors",
		"`POST /api/v1/tasks/:id/cancel`": "post-apiv1tasksidcancel",
		"Tags & workers: a superset":      "tags--workers-a-superset",
	} {
		if got := Anchor(in); got != want {
			t.Errorf("Anchor(%q) = %q, want %q", in, got, want)
		}
	}
}

// blockingFS holds every read until release is closed.
type blockingFS struct {
	fs.FS
	release chan struct{}
}

func (b blockingFS) Open(name string) (fs.File, error) {
	<-b.release
	return b.FS.Open(name)
}

// The first search during an index build waits for the build instead of
// returning nothing.
func TestSearch_WaitsForBuild(t *testing.T) {
	release := make(chan struct{})
	SetFS(blockingFS{os.DirFS("../../docs"), release})
	defer SetFS(os.DirFS("../../docs"))

	Warm()
	done := make(chan []Result)
	go func() {
		res, _ := Search("cancel task", 5)
		done <- res
	}()
	select {
	case <-done:
		t.Fatal("Search returned before the index build could finish")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if res := <-done; len(res) == 0 {
		t.Error("Search returned no results after waiting for the build")
	}
}

func BenchmarkBuildIndex(b *testing.B) {
	f := os.DirFS("../../docs")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := BuildIndex(f); err != nil {
			b.Fatal(err)
		}
	}
}
