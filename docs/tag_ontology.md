# Tag Ontology

Tags are **constraints, not labels.** A task type's tags say which
workers may run its tasks, the way a Kubernetes `nodeSelector` or a
Nomad `constraint` does. Every tag added to a type *narrows* the set of
workers that can claim it.

## How tags match workers

A worker claims a task only if the worker's tags are a **superset** of
the task's tags: every tag on the task must appear in the worker's tag
set. The worker may advertise more.

- A task's tags are copied from its task type when it is submitted; a
  task in a recurring series copies the template's.
- Tags are compared as exact strings. There is no hierarchy or
  wildcard: `os:unix` on a worker does not satisfy `os:linux` on a task.
- A task with no tags can be claimed by any worker.
- A task whose tags no registered worker satisfies stays `WAITING`.
  `blanket task-validate --check-workers` warns about such a type (code
  014), and [`GET /worker/`](api.md#workers) lists what each worker
  advertises.

So **workers should advertise generously, and tasks should request the
minimum.** A Linux worker can advertise both `os:linux` and `os:unix`; a
task tagged only `os:unix` stays claimable by Linux and macOS workers,
while one tagged `os:linux` narrows to Linux.

## Namespaced tags

Tags take the form `namespace:value`. The convention keeps what a task
needs to *run* visually distinct from who is *allowed* to run it, and
lets `task-validate` catch typos. The built-in seed vocabulary:

| Namespace | Purpose | Examples |
| --- | --- | --- |
| `os:` | operating system / family | `os:linux`, `os:darwin`, `os:windows`, `os:unix` |
| `exec:` | shell or interpreter | `exec:bash`, `exec:cmd`, `exec:powershell` |
| `runtime:` | toolchain on the host | `runtime:python3`, `runtime:node`, `runtime:docker` |
| `resource:` | hardware / network needs | `resource:gpu`, `resource:bigmem`, `resource:internet` |
| `env:` | deployment tier | `env:prod`, `env:staging` |
| `team:` | ownership routing — a natural fit for AD groups | `team:data-eng` |
| `cost:` | cost-center routing | `cost:rnd` |
| `access:` | privilege routing — a natural fit for AWS IAM capabilities | `access:secrets`, `access:vpn` |

The seed list is a starting point, not a closed set: add your own
namespaces and values (next section). An unnamespaced tag (`bash`,
`unix`) also works — nothing enforces the convention — but the lint
nudges toward it (code 011).

## Extending the vocabulary: `known-tags.conf`

`blanket task-validate` resolves a **known-tag set** from three sources.
Any one of them makes a tag known:

1. **The built-in seed vocabulary** above (`--no-builtin-tags` drops it).
2. **`.blanket/known-tags.conf`** and **`.blanket/known-tags.d/*.conf`**
   beside each directory in `tasks.typesPaths`, merged across all of
   them. One tag per line; blank lines and `#` comments are ignored.

   ```
   # .blanket/known-tags.d/mytags.conf
   team:platform
   team:infra
   cost:marketing
   ```

3. **Tags already in use** on any loaded task type. One occurrence is
   enough, so a new namespaced tag is silent by default unless it is
   within two edits of an existing one (code 010).

`blanket task-validate --dump-known-tags` prints the resolved set with
each tag's origin (`builtin`, `file` or `observed`): what this
deployment recognizes, and what an authoring tool should recommend from.

## The tag lint: codes 010-014

The known-tag set feeds `blanket task-validate`'s tag checks. Near-miss
and unnamespaced-tag warnings are on by default; the new-tag,
undeclared-tag and no-matching-worker checks are opt-in. The check
table and flags are in
[task_type_definitions.md](task_type_definitions.md#tag-lint-codes-010-014).
