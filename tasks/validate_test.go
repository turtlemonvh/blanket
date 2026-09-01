package tasks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// mustLoad parses a task type TOML fragment for a test, ignoring the
// load error so tests can exercise malformed configs (e.g. missing
// command) the same way ReadTaskTypeFromFilepathForValidation does.
func mustLoad(t *testing.T, toml string) (*TaskType, error) {
	t.Helper()
	tt, err := ReadTaskType(strings.NewReader(toml))
	tt.Config.Set("name", "test_type")
	return &tt, err
}

func findingCodes(findings []Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, f := range findings {
		codes = append(codes, f.Code)
	}
	return codes
}

func TestValidateTaskType_Clean(t *testing.T) {
	tt, loadErr := mustLoad(t, `
description = "does a thing"
documentation = "more about the thing"
tags = ["bash", "unix"]
executor = "bash"
command = "echo {{.NAME}}"

[[environment.default]]
name = "NAME"
value = "world"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Empty(t, findings, "expected a fully-documented, well-formed type to be clean: %+v", findings)
}

func TestValidateTaskType_001_MissingCommand(t *testing.T) {
	tt, loadErr := mustLoad(t, `
tags = ["bash"]
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "001")
	for _, f := range findings {
		if f.Code == "001" {
			assert.Equal(t, LevelError, f.Level)
		}
	}
}

func TestValidateTaskType_002_UnknownExecutor(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo hi"
executor = "definitely-not-a-real-binary-xyz"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "002")
}

func TestValidateTaskType_003_BadTemplate(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo {{.NAME"
executor = "bash"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "003")
	for _, f := range findings {
		if f.Code == "003" {
			assert.Equal(t, LevelError, f.Level)
		}
	}
}

func TestValidateTaskType_004_UndeclaredTemplateRef(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo {{.UNDECLARED}}"
executor = "bash"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "004")
	for _, f := range findings {
		if f.Code == "004" {
			assert.Equal(t, LevelWarn, f.Level)
			assert.Contains(t, f.Message, "UNDECLARED")
		}
	}
}

func TestValidateTaskType_004_DeclaredRefIsClean(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo {{.NAME}}"
executor = "bash"

[[environment.default]]
name = "NAME"
value = "world"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.NotContains(t, findingCodes(findings), "004")
}

func TestValidateTaskType_005_UnusedRequiredInput(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo hello"
executor = "bash"

[[environment.required]]
name = "UNUSED"
description = "never referenced"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "005")
}

func TestValidateTaskType_005_UsedRequiredInputIsClean(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo {{.USED}}"
executor = "bash"

[[environment.required]]
name = "USED"
description = "referenced"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.NotContains(t, findingCodes(findings), "005")
}

func TestValidateTaskType_006_MissingDescription(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo hi"
executor = "bash"
documentation = "some docs"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "006")
	assert.NotContains(t, findingCodes(findings), "007")
}

func TestValidateTaskType_007_MissingDocumentation(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo hi"
executor = "bash"
description = "some description"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "007")
	assert.NotContains(t, findingCodes(findings), "006")
}

func TestValidateTaskType_008_TooManyInputs(t *testing.T) {
	var b strings.Builder
	b.WriteString("command = \"echo hi\"\nexecutor = \"bash\"\ndescription = \"d\"\ndocumentation = \"d\"\n")
	for i := 0; i < 11; i++ {
		b.WriteString("[[environment.optional]]\n")
		b.WriteString("name = \"VAR")
		b.WriteString(string(rune('A' + i)))
		b.WriteString("\"\n")
	}
	tt, loadErr := mustLoad(t, b.String())
	findings := ValidateTaskType(tt, loadErr)
	assert.Contains(t, findingCodes(findings), "008")
}

func TestValidateTaskType_008_HealthyInputCountIsClean(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = "echo {{.A}} {{.B}}"
executor = "bash"
description = "d"
documentation = "d"

[[environment.default]]
name = "A"
value = "1"

[[environment.default]]
name = "B"
value = "2"
`)
	findings := ValidateTaskType(tt, loadErr)
	assert.NotContains(t, findingCodes(findings), "008")
}

func TestValidateTaskType_IfAndRangeRefsAreDetected(t *testing.T) {
	tt, loadErr := mustLoad(t, `
command = '''
{{if .FLAG}}yes{{end}}
{{range .LIST}}{{.}}{{end}}
'''
executor = "bash"
`)
	findings := ValidateTaskType(tt, loadErr)
	codes004 := 0
	var messages []string
	for _, f := range findings {
		if f.Code == "004" {
			codes004++
			messages = append(messages, f.Message)
		}
	}
	assert.Equal(t, 2, codes004, "expected both {{if .FLAG}} and {{range .LIST}} to be flagged: %v", messages)
}
