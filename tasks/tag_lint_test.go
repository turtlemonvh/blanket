package tasks

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func mustLoadTagged(t *testing.T, name string, tags ...string) TaskType {
	t.Helper()
	quoted := make([]string, len(tags))
	for i, tag := range tags {
		quoted[i] = strconv.Quote(tag)
	}
	toml := fmt.Sprintf("command = \"echo hi\"\ntags = [%s]\n", strings.Join(quoted, ", "))
	tt, err := ReadTaskType(strings.NewReader(toml))
	if err != nil {
		t.Fatalf("failed to load test task type: %v", err)
	}
	tt.Config.Set("name", name)
	return tt
}

func lintFindingCodes(findings []Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, f := range findings {
		codes = append(codes, f.Code)
	}
	return codes
}

func TestLintTags_010_NearMiss(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "os:linu") // one char short of os:linux
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{})
	assert.Contains(t, lintFindingCodes(findings), "010")
	for _, f := range findings {
		if f.Code == "010" {
			assert.Contains(t, f.Message, "os:linu")
			assert.Contains(t, f.Suggestion, "os:linux")
		}
	}
}

func TestLintTags_010_ExactKnownTagIsClean(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "os:linux")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{})
	assert.Empty(t, findings, "an exact, known tag should never be flagged: %+v", findings)
}

func TestLintTags_010_NovelNamespacedTagIsSilentByDefault(t *testing.T) {
	// team:platform isn't in the seed vocabulary and isn't a near-miss of
	// anything — introducing it for the first time must be silent.
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{})
	assert.Empty(t, findings, "a novel well-formed namespaced tag should be silent: %+v", findings)
}

func TestLintTags_011_UnnamespacedWithNamespacedMatch(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "bash")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{}) // seed vocab has exec:bash
	findings := LintTags(&tt, idx, TagLintOptions{})
	assert.Contains(t, lintFindingCodes(findings), "011")
	assert.NotContains(t, lintFindingCodes(findings), "010", "011 should suppress the generic near-miss check for the same tag")
	for _, f := range findings {
		if f.Code == "011" {
			assert.Contains(t, f.Suggestion, "exec:bash")
		}
	}
}

func TestLintTags_012_NewTag_OffByDefault(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{}) // WarnNewTag not set
	assert.NotContains(t, lintFindingCodes(findings), "012")
}

func TestLintTags_012_NewTag_WarnNewTag(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{WarnNewTag: true})
	assert.Contains(t, lintFindingCodes(findings), "012")
}

func TestLintTags_012_TagUsedByAnotherTypeIsNotNew(t *testing.T) {
	other := mustLoadTagged(t, "other", "team:platform")
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{other, tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{WarnNewTag: true})
	assert.NotContains(t, lintFindingCodes(findings), "012",
		"a tag used by another loaded type should not count as new, even though it's not builtin/file-declared")
}

func TestLintTags_012_SelfUsageDoesNotCountAsElsewhere(t *testing.T) {
	// A tag used only by the type being checked (not by any *other* type,
	// not builtin, not file-declared) must still be flagged as new.
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{WarnNewTag: true})
	assert.Contains(t, lintFindingCodes(findings), "012")
}

func TestLintTags_013_UndeclaredTag_OffByDefault(t *testing.T) {
	other := mustLoadTagged(t, "other", "team:platform")
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{other, tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{}) // WarnUndeclaredTag not set
	assert.NotContains(t, lintFindingCodes(findings), "013")
}

func TestLintTags_013_UndeclaredTag_StricterThan012(t *testing.T) {
	// team:platform is used by another type (so 012 wouldn't fire even if
	// enabled), but it's not in the built-in vocab or a known-tags file —
	// 013 should still catch it.
	other := mustLoadTagged(t, "other", "team:platform")
	tt := mustLoadTagged(t, "t1", "team:platform")
	idx := BuildTagIndex(nil, []TaskType{other, tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{WarnUndeclaredTag: true})
	assert.Contains(t, lintFindingCodes(findings), "013")
}

func TestLintTags_013_BuiltinTagIsDeclared(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "os:linux")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{WarnUndeclaredTag: true})
	assert.NotContains(t, lintFindingCodes(findings), "013")
}

func TestLintTags_014_NoWorkerSatisfies(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "os:linux", "exec:bash")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{
		CheckWorkers:  true,
		WorkerTagSets: [][]string{{"os:darwin", "exec:bash"}}, // missing os:linux
	})
	assert.Contains(t, lintFindingCodes(findings), "014")
}

func TestLintTags_014_WorkerSatisfies(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "os:linux", "exec:bash")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{
		CheckWorkers:  true,
		WorkerTagSets: [][]string{{"os:linux", "exec:bash", "team:data-eng"}}, // superset
	})
	assert.NotContains(t, lintFindingCodes(findings), "014")
}

func TestLintTags_014_NoWorkersAtAllFails(t *testing.T) {
	tt := mustLoadTagged(t, "t1") // no tags required at all
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{
		CheckWorkers:  true,
		WorkerTagSets: nil,
	})
	assert.Contains(t, lintFindingCodes(findings), "014", "zero registered workers means nothing can claim anything")
}

func TestLintTags_014_NotRunWhenDisabled(t *testing.T) {
	tt := mustLoadTagged(t, "t1", "os:linux")
	idx := BuildTagIndex(nil, []TaskType{tt}, KnownTagsOptions{})
	findings := LintTags(&tt, idx, TagLintOptions{}) // CheckWorkers false
	assert.NotContains(t, lintFindingCodes(findings), "014")
}

func TestAnyWorkerSatisfies(t *testing.T) {
	cases := []struct {
		name     string
		workers  [][]string
		taskTags []string
		want     bool
	}{
		{"empty workers, empty tags", nil, nil, false},
		{"empty workers, some tags", nil, []string{"a"}, false},
		{"worker exact match", [][]string{{"a", "b"}}, []string{"a", "b"}, true},
		{"worker superset", [][]string{{"a", "b", "c"}}, []string{"a", "b"}, true},
		{"worker missing one tag", [][]string{{"a"}}, []string{"a", "b"}, false},
		{"second worker satisfies", [][]string{{"a"}, {"a", "b"}}, []string{"a", "b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, anyWorkerSatisfies(c.workers, c.taskTags))
		})
	}
}
