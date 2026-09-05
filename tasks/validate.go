package tasks

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
)

// Finding levels. Kept to two so --strict has a simple, predictable
// meaning: exit non-zero on any warning too, in addition to errors.
const (
	LevelError = "error"
	LevelWarn  = "warn"
)

// Finding is one result of validating a task type. Codes are stable once
// assigned — see CheckDescriptions for what each one means.
type Finding struct {
	Type       string `json:"type"`  // task type name
	Code       string `json:"code"`  // e.g. "001"
	Level      string `json:"level"` // "error" or "warn"
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// CheckDescriptions documents what each coded check means, keyed by code.
// Used for --help text and docs/task_type_definitions.md. Codes 010+ are
// the tag lint (see docs/tag_ontology.md and tasks/tag_lint.go).
var CheckDescriptions = map[string]string{
	"001": "command is present and non-empty",
	"002": "executor resolves on $PATH",
	"003": "command parses as a Go template",
	"004": "template refs are all declared inputs",
	"005": "a required input is never referenced by command",
	"006": "description is present and non-empty",
	"007": "documentation is present and non-empty",
	"008": "declared input count is in the healthy range (2-5)",
	"009": "result_file is a relative path contained in the result dir",
	"010": "tag is a near-miss (edit distance <=2) of a known tag",
	"011": "unnamespaced tag has a namespaced value-match (e.g. bash -> exec:bash)",
	"012": "tag is new: not declared anywhere, not used by any other type (opt-in)",
	"013": "tag isn't declared in the known-tags vocabulary (opt-in, stricter than 012)",
	"014": "no registered worker advertises a superset of this type's tags (opt-in)",
}

// inputCountWarn and inputCountStrongWarn bound the healthy range for a
// task type's declared inputs (env vars across default/required/optional,
// deduplicated by name). Task types don't declare file inputs in their
// TOML — file uploads are an ad-hoc submit-time mechanism (see
// docs/usage.md) — so only env vars are countable here.
const (
	inputCountWarn       = 10
	inputCountStrongWarn = 20
)

// ValidateTaskType runs every check (001-008) against a task type and
// returns the findings, in code order. loadErr is the error returned by
// the type's load path, if any (e.g. a missing `command` field surfaces
// here rather than silently dropping the type) — pass nil if the type
// loaded cleanly.
func ValidateTaskType(tt *TaskType, loadErr error) []Finding {
	name := tt.GetName()
	var findings []Finding

	command := tt.Config.GetString("command")

	// 001: command present and non-empty.
	if command == "" {
		msg := "command is missing or empty"
		if loadErr != nil {
			msg = fmt.Sprintf("command is missing or empty: %s", loadErr.Error())
		}
		findings = append(findings, Finding{Type: name, Code: "001", Level: LevelError, Message: msg})
	}

	// 002: executor resolves on $PATH.
	executor := tt.Config.GetString("executor")
	if executor == "" {
		executor = "bash"
	}
	if _, err := exec.LookPath(executor); err != nil {
		findings = append(findings, Finding{
			Type: name, Code: "002", Level: LevelError,
			Message: fmt.Sprintf("executor %q not found on $PATH", executor),
		})
	}

	// 003: command parses as a Go template. Only meaningful when there's a
	// command to parse — 001 already covers the empty case.
	var refs []string
	if command != "" {
		tmpl, err := template.New(name).Parse(command)
		if err != nil {
			findings = append(findings, Finding{
				Type: name, Code: "003", Level: LevelError,
				Message: fmt.Sprintf("command does not parse as a Go template: %s", err.Error()),
			})
		} else {
			refs = templateFieldRefs(tmpl)
		}
	}

	declared, declaredOrder := declaredEnvNames(tt)

	// 004: every {{.VAR}} ref should be a declared input. Warn, not error:
	// a ref may legitimately resolve to a host-inherited env var rather
	// than one declared in this type's `environment` table.
	for _, ref := range refs {
		if !declared[ref] {
			findings = append(findings, Finding{
				Type: name, Code: "004", Level: LevelWarn,
				Message:    fmt.Sprintf("command references {{.%s}}, which is not a declared input", ref),
				Suggestion: "declare it under [[environment.default]], [[environment.required]], or [[environment.optional]], or ignore this if it's inherited from the worker's environment",
			})
		}
	}

	// 005: a required input that's never referenced is dead config.
	refSet := map[string]bool{}
	for _, r := range refs {
		refSet[r] = true
	}
	for _, ev := range tt.EnvNames("required") {
		if !refSet[ev] {
			findings = append(findings, Finding{
				Type: name, Code: "005", Level: LevelWarn,
				Message: fmt.Sprintf("required input %q is never referenced by command", ev),
			})
		}
	}

	// 006 / 007: description / documentation present.
	if strings.TrimSpace(tt.GetDescription()) == "" {
		findings = append(findings, Finding{
			Type: name, Code: "006", Level: LevelWarn,
			Message: "description is missing or empty",
		})
	}
	if strings.TrimSpace(tt.GetDocumentation()) == "" {
		findings = append(findings, Finding{
			Type: name, Code: "007", Level: LevelWarn,
			Message: "documentation is missing or empty",
		})
	}

	// 008: input count in the healthy range.
	n := len(declaredOrder)
	switch {
	case n > inputCountStrongWarn:
		findings = append(findings, Finding{
			Type: name, Code: "008", Level: LevelWarn,
			Message:    fmt.Sprintf("%d declared inputs is well past the healthy range (2-5)", n),
			Suggestion: "strongly consider splitting this into multiple task types",
		})
	case n > inputCountWarn:
		findings = append(findings, Finding{
			Type: name, Code: "008", Level: LevelWarn,
			Message:    fmt.Sprintf("%d declared inputs is more than the healthy range (2-5)", n),
			Suggestion: "consider whether some of these could be fixed defaults instead",
		})
	}

	// 009: result_file is a relative path contained in the result dir.
	// Error, not warning: an invalid value makes the whole type
	// unloadable (ReadTaskType rejects it), so the type isn't servable
	// until it's fixed.
	if _, err := CleanResultFile(tt.Config.GetString("result_file")); err != nil {
		findings = append(findings, Finding{
			Type: name, Code: "009", Level: LevelError,
			Message:    err.Error(),
			Suggestion: "use a path relative to the task's result directory, e.g. result_file = \"result.json\"",
		})
	}

	return findings
}

// declaredEnvNames returns the set (and stable, deduplicated order) of
// every input name declared under environment.{default,required,optional}.
func declaredEnvNames(tt *TaskType) (map[string]bool, []string) {
	set := map[string]bool{}
	var order []string
	for _, section := range []string{"default", "required", "optional"} {
		for _, name := range tt.EnvNames(section) {
			if set[name] {
				continue
			}
			set[name] = true
			order = append(order, name)
		}
	}
	return set, order
}

// templateFieldRefs walks a parsed template's AST and returns the sorted,
// deduplicated set of top-level dot-field names referenced (e.g. {{.NAME}},
// {{if .NAME}}, {{range .NAME}}). It's a heuristic, not a full data-flow
// analysis — good enough for a warn-level check.
func templateFieldRefs(tmpl *template.Template) []string {
	seen := map[string]bool{}
	var refs []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			refs = append(refs, name)
		}
	}

	var walkPipe func(p *parse.PipeNode)
	var walk func(n parse.Node)

	walkPipe = func(p *parse.PipeNode) {
		if p == nil {
			return
		}
		for _, cmd := range p.Cmds {
			for _, arg := range cmd.Args {
				walk(arg)
			}
		}
	}

	walk = func(n parse.Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *parse.ListNode:
			// IfNode/RangeNode/WithNode pass a nil *parse.ListNode for a
			// missing {{else}} branch — nil as a concrete type inside the
			// parse.Node interface, so the `n == nil` check above doesn't
			// catch it. Guard here instead of at every call site.
			if x == nil {
				return
			}
			for _, c := range x.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walkPipe(x.Pipe)
		case *parse.FieldNode:
			if len(x.Ident) > 0 {
				add(x.Ident[0])
			}
		case *parse.IfNode:
			walkPipe(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.RangeNode:
			walkPipe(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.WithNode:
			walkPipe(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.TemplateNode:
			walkPipe(x.Pipe)
		}
	}

	if tmpl.Tree != nil {
		walk(tmpl.Tree.Root)
	}
	sort.Strings(refs)
	return refs
}
