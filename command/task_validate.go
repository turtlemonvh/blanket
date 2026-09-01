package command

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
	"github.com/turtlemonvh/blanket/tasks"
)

var (
	taskValidateJSON              bool
	taskValidateStrict            bool
	taskValidateDumpKnownTags     bool
	taskValidateNoBuiltinTags     bool
	taskValidateWarnNewTag        bool
	taskValidateWarnUndeclaredTag bool
	taskValidateCheckWorkers      bool
)

var taskValidateCmd = &cobra.Command{
	Use:   "task-validate [type-name]",
	Short: "Validate that task types are well-formed and runnable",
	Long: `Checks each task type against a set of coded rules: the command is
present and parses as a Go template, the executor resolves on $PATH,
template references match declared inputs, and description/documentation/
input-count fall within the recommended range (codes 001-008); plus a tag
lint against the resolved vocabulary — near-miss and unnamespaced-tag
detection are on by default, new/undeclared-tag and worker-existence
checks are opt-in (codes 010-014). See docs/task_type_definitions.md for
what each code means.

Exit code is non-zero if any error-level finding exists, or (with
--strict) if any warning exists either.

Use --dump-known-tags to print the resolved tag vocabulary instead of
validating — the built-in seed tags (unless --no-builtin-tags), entries
from .blanket/known-tags.conf and .blanket/known-tags.d/*.conf beside each
configured types directory, and tags observed in loaded task types.`,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()

		typesDirs := viper.GetStringSlice("tasks.typesPaths")

		if taskValidateDumpKnownTags {
			runDumpKnownTags(typesDirs)
			return
		}

		tts, loadErrs := tasks.ReadTaskTypesForValidation(typesDirs)

		// Build the tag index from every loaded type before any
		// name-filtering below — "is this tag used elsewhere" (code 012)
		// needs to see the full picture, not just the type(s) being
		// printed.
		tagIdx := tasks.BuildTagIndex(typesDirs, tts, tasks.KnownTagsOptions{
			NoBuiltinTags: taskValidateNoBuiltinTags,
		})
		lintOpts := tasks.TagLintOptions{
			WarnNewTag:        viper.GetBool("tasks.warnNewTag"),
			WarnUndeclaredTag: viper.GetBool("tasks.warnUndeclaredTag"),
		}

		var allFindings []tasks.Finding
		if taskValidateCheckWorkers {
			sets, err := client.GetActiveWorkerTagSets(viper.GetInt("port"))
			if err != nil {
				allFindings = append(allFindings, tasks.Finding{
					Code: "014", Level: tasks.LevelWarn,
					Message: fmt.Sprintf("worker-existence check (014) skipped: could not reach server at http://localhost:%d (%s)", viper.GetInt("port"), err.Error()),
				})
			} else {
				lintOpts.CheckWorkers = true
				lintOpts.WorkerTagSets = sets
			}
		}

		if len(args) > 0 {
			name := args[0]
			var filteredTts []tasks.TaskType
			var filteredErrs []error
			for i := range tts {
				if tts[i].GetName() == name {
					filteredTts = append(filteredTts, tts[i])
					filteredErrs = append(filteredErrs, loadErrs[i])
				}
			}
			if len(filteredTts) == 0 {
				fmt.Fprintf(os.Stderr, "Task type %q not found\n", name)
				os.Exit(1)
			}
			tts, loadErrs = filteredTts, filteredErrs
		}

		// Sort by name for deterministic output (ReadTaskTypesForValidation
		// returns directory-scan order, which isn't stable across hosts).
		order := make([]int, len(tts))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return tts[order[a]].GetName() < tts[order[b]].GetName() })

		perType := map[string][]tasks.Finding{}
		sortedTts := make([]tasks.TaskType, len(tts))
		for i, idx := range order {
			sortedTts[i] = tts[idx]
			f := tasks.ValidateTaskType(&tts[idx], loadErrs[idx])
			f = append(f, tasks.LintTags(&tts[idx], tagIdx, lintOpts)...)
			allFindings = append(allFindings, f...)
			perType[tts[idx].GetName()] = f
		}

		if taskValidateJSON {
			printFindingsJSON(allFindings)
		} else {
			printFindingsTable(sortedTts, perType, allFindings)
		}

		anyError, anyWarn := false, false
		for _, f := range allFindings {
			switch f.Level {
			case tasks.LevelError:
				anyError = true
			case tasks.LevelWarn:
				anyWarn = true
			}
		}
		if anyError || (taskValidateStrict && anyWarn) {
			os.Exit(1)
		}
	},
}

func printFindingsJSON(findings []tasks.Finding) {
	if findings == nil {
		findings = []tasks.Finding{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(findings); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding findings: %s\n", err)
		os.Exit(1)
	}
}

// printFindingsTable prints the per-type status table plus each finding
// below it. allFindings additionally carries any finding not tied to a
// specific type (Type == "") — currently just the 014-skipped notice when
// --check-workers can't reach a server — printed in its own section since
// it won't show up under any type in perType.
func printFindingsTable(tts []tasks.TaskType, perType map[string][]tasks.Finding, allFindings []tasks.Finding) {
	for _, f := range allFindings {
		if f.Type == "" {
			fmt.Printf("%s %s: %s\n", f.Code, f.Level, f.Message)
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTAGS\tSTATUS")

	for i := range tts {
		name := tts[i].GetName()
		tags := strings.Join(tts[i].Config.GetStringSlice("tags"), " ")
		findings := perType[name]

		errCount, warnCount := 0, 0
		for _, f := range findings {
			if f.Level == tasks.LevelError {
				errCount++
			} else {
				warnCount++
			}
		}
		status := "ok"
		switch {
		case errCount > 0 && warnCount > 0:
			status = fmt.Sprintf("FAIL (%d errors, %d warnings)", errCount, warnCount)
		case errCount > 0:
			status = fmt.Sprintf("FAIL (%d errors)", errCount)
		case warnCount > 0:
			status = fmt.Sprintf("ok (%d warnings)", warnCount)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, tags, status)
	}
	w.Flush()

	printedAny := false
	for i := range tts {
		for _, f := range perType[tts[i].GetName()] {
			if !printedAny {
				fmt.Println()
				printedAny = true
			}
			fmt.Printf("%s %s %s: %s\n", f.Code, f.Level, f.Type, f.Message)
			if f.Suggestion != "" {
				fmt.Printf("     -> %s\n", f.Suggestion)
			}
		}
	}
}

func runDumpKnownTags(typesDirs []string) {
	tts, _ := tasks.ReadTaskTypesForValidation(typesDirs)
	known := tasks.ResolveKnownTags(typesDirs, tts, tasks.KnownTagsOptions{
		NoBuiltinTags: taskValidateNoBuiltinTags,
	})

	if taskValidateJSON {
		if known == nil {
			known = []tasks.KnownTag{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(known); err != nil {
			fmt.Fprintf(os.Stderr, "error encoding known tags: %s\n", err)
			os.Exit(1)
		}
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TAG\tORIGIN")
	for _, k := range known {
		fmt.Fprintf(w, "%s\t%s\n", k.Tag, k.Origin)
	}
	w.Flush()
}

func init() {
	taskValidateCmd.Flags().BoolVar(&taskValidateJSON, "json", false, "print findings as JSON instead of a table")
	taskValidateCmd.Flags().BoolVar(&taskValidateStrict, "strict", false, "exit non-zero on warnings too, not just errors")
	taskValidateCmd.Flags().BoolVar(&taskValidateDumpKnownTags, "dump-known-tags", false, "print the resolved tag vocabulary (built-in + .blanket files + observed) instead of validating")
	taskValidateCmd.Flags().BoolVar(&taskValidateNoBuiltinTags, "no-builtin-tags", false, "exclude the built-in seed vocabulary when resolving known tags")
	taskValidateCmd.Flags().BoolVar(&taskValidateWarnNewTag, "warn-new-tag", false, "warn (code 012) on a tag that's not declared anywhere and not used by any other task type")
	taskValidateCmd.Flags().BoolVar(&taskValidateWarnUndeclaredTag, "warn-undeclared-tag", false, "warn (code 013) on a tag that isn't declared in a known-tags file, even if used elsewhere")
	taskValidateCmd.Flags().BoolVar(&taskValidateCheckWorkers, "check-workers", false, "warn (code 014) when no registered worker could claim a type's tags; requires a reachable server")

	// tasks.warnNewTag / tasks.warnUndeclaredTag can also be set in the
	// blanket config file, so a deployment can make either check its
	// default without every invocation needing the flag.
	viper.BindPFlag("tasks.warnNewTag", taskValidateCmd.Flags().Lookup("warn-new-tag"))
	viper.BindPFlag("tasks.warnUndeclaredTag", taskValidateCmd.Flags().Lookup("warn-undeclared-tag"))
}
