---
name: blanket-task-type
description: Use when creating or editing a blanket task type (a TOML file under a tasks.typesPaths directory) — writing a new one from a description of what should run, or fixing/improving an existing one. Triggers on phrasings like "add a blanket task type for X", "write a task type that runs Y", "make this task type submittable with fewer inputs", or edits to any *.toml file under a directory blanket is configured to read task types from.
---

# blanket-task-type

Authors and improves blanket task type TOML files, gated on
`blanket task-validate`. This file is deliberately thin — the schema, the
`{{.VAR}}` vs `$VAR` distinction, the tag ontology, and worked examples
all live in the repo docs below; read them rather than relying on
memory, since they're the source of truth and can change independently
of this skill.

**Read first:**

- `docs/authoring_task_types.md` — the full authoring guide: sizing,
  tags, common patterns, the loop below in more detail.
- `docs/tag_ontology.md` — the namespaced tag convention and why tags
  are worker constraints, not labels.
- `docs/task_type_definitions.md` — the formal schema and the
  `task-validate` check table.

## The four quality gates

Every task type this skill produces or edits should pass all four, or
have an explicit, stated reason it doesn't:

1. **Valid Go template syntax** in `command` (`task-validate` code 003).
2. **Good tags** — namespaced, reusing what's already in the vocabulary
   where it fits (codes 010/011/012/013).
3. **2-5 inputs** — env vars plus any expected file uploads (code 008).
4. **Clear documentation** — `description` and `documentation` set
   (codes 006/007).

## Workflow

1. **Read the guide docs above** if this is the first task type touched
   in the session, or if it's been a while — the schema and ontology can
   drift.
2. **Discover context.** Find the configured `tasks.typesPaths` (the
   server config, or ask the user) and glob it for existing `*.toml`
   files worth copying a pattern from. Run
   `blanket task-validate --dump-known-tags` to see the real tag
   vocabulary in use in this deployment, not just the seed list.
3. **Understand the task.** What command needs to run; what it requires
   (interpreter, OS, network, secrets); which parts genuinely vary
   between submissions versus which should be fixed defaults. Push back
   on turning something into an input just because it *could* vary — the
   2-5 target is a real constraint, not a suggestion.
4. **Write the TOML** (or edit the existing one) — `description`,
   `documentation`, namespaced `tags`, a templated `command`, and a small
   `environment` table.
5. **Validate — required, not optional:**
   ```bash
   blanket task-validate --json <type-name>
   ```
   Fix every error. For each warning, either fix it or state a specific
   reason not to (e.g. "this code-004 warning is expected — `$REGION` is
   inherited from the worker's environment, not declared here"). Iterate
   until `task-validate` is clean or every remaining warning is
   justified.
6. **Smoke-test.** Submit a real task against the type
   (`blanket submit -t <type-name>` or `POST /task/`) and confirm it
   reaches `SUCCESS`, not just that `task-validate` is happy — validation
   catches structural problems, not runtime ones (a missing binary the
   executor check didn't catch because it only checks the *executor*
   itself, not what the command shells out to; a typo'd flag; etc).
7. **Report** what was created/changed, the tags chosen and why, and the
   final `task-validate` output.

## Common failure modes to avoid

- **Inventing a new tag when a close one already exists.** Always check
  `--dump-known-tags` first; `task-validate` will warn on near-misses
  (010) but won't stop you from creating `runtime:py3` next to an
  existing `runtime:python3` if you never look.
- **Over-parameterizing.** A type with 15 optional env vars is worse than
  three focused types with 3-4 each. If code 008 warns, don't just
  suppress it — actually reconsider the shape.
- **Skipping validation because the type "looks right".** Template syntax
  errors and missing executors are exactly the kind of thing that's easy
  to eyeball past and only catches at first execution. Run
  `task-validate` every time, not just when something seems off.
