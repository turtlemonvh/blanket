# Tag Ontology

Tags are **constraints, not labels.** A worker only claims a task whose
tags it satisfies — `lib/bolt/database_util.go` requires every task tag to
be present in the claiming worker's tag set. Adding a tag to a task type
therefore *narrows* which workers may run it; it's closer to a Kubernetes
`nodeSelector` or a Nomad `constraint` than a descriptive label.

The practical consequence: **workers should advertise generously, and
tasks should request the minimum.** A Linux worker can advertise both
`os:linux` and `os:unix`; a task tagged only `os:unix` stays claimable by
both Linux and macOS workers, while a task tagged `os:linux` narrows to
Linux workers specifically.

## Namespaced tags

Tags take the form `namespace:value`. Namespacing keeps unrelated
concerns — what a task needs to *run*, versus who's *allowed* to run it —
visually distinct, and lets `task-validate` catch typos (see below).

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

This seed list ships built into blanket, but it's a starting point, not a
closed set. **You're expected to add your own namespaces and values** —
see below.

Unnamespaced tags (`bash`, `unix`, `windows`) still work; nothing enforces
the `namespace:value` format. The examples under `examples/types/` predate
this convention and haven't been migrated (tracked separately) — they
remain valid, functioning task types.

## Extending the vocabulary

`blanket task-validate` resolves a **known-tag set** from three sources,
each of which is enough on its own for a tag to count as known:

1. **The built-in seed vocabulary** above (disable with `--no-builtin-tags`).
2. **`.blanket/known-tags.conf`** and **`.blanket/known-tags.d/*.conf`**,
   read beside each directory listed in `tasks.typesPaths` and merged
   across all of them. One tag per line; blank lines and `#` comments are
   ignored.

   ```
   # .blanket/known-tags.d/mytags.conf
   team:platform
   team:infra
   cost:marketing
   ```

3. **Tags already in use** — any tag that appears on any loaded task type
   counts as known. A tag needs to show up once to be self-documenting;
   introducing a brand-new well-formed namespaced tag is never flagged.

Run `blanket task-validate --dump-known-tags` to print the resolved set
along with each tag's origin (`builtin`, `file`, or `observed`) — useful
for checking what a given deployment actually recognizes, or for an
authoring tool deciding what to recommend.

## What's next

The known-tag set feeds a lint pass (near-miss detection, strictness
flags for new/undeclared tags, worker-existence checks) — see the
task-validate check table in
[task_type_definitions.md](task_type_definitions.md) for which codes are
implemented today.
