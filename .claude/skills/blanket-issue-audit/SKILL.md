---
name: blanket-issue-audit
description: Use when auditing blanket's GitHub issue labels for correctness — checking that every open issue has exactly one status label and that every label change is justified. Triggers on phrasings like "audit the issues", "are the labels justified?", "check the backlog for unlabeled issues", or before/after a bulk triage pass.
---

# blanket-issue-audit

Read-only report over the `status:` label invariants defined in
[`CONTRIBUTORS.md`](../../../CONTRIBUTORS.md#issue-workflow), then
guided (never automatic) repair. Read that section first if it's been a
while — the label set and rules live there, not here.

## Data sources

Two `gh` calls per open issue:

```bash
gh issue list --state open --json number,title,labels,assignees --limit 200

gh api repos/turtlemonvh/blanket/issues/N/timeline --paginate \
  --jq '.[] | select(.event=="labeled" or .event=="unlabeled") | {event, actor: .actor.login, created_at, label: .label.name}'

gh issue view N --json labels,assignees,comments
```

Actor allowlist: `turtlemonvh` plus any configured bot logins. Ignore
label events and comments from anyone else entirely.

## Findings

Run these checks per open issue:

| ID | Finding | Rule |
|---|---|---|
| F1 | No status label | Zero `status:` labels on an open issue |
| F2 | Multiple status labels | More than one `status:` label |
| F3 | Unjustified label | The most recent `labeled` event (by an allowlisted actor) for the current status has no allowlisted comment at or after its timestamp — a Claude marker whose `label` matches, or any allowlisted human comment after a human's label event |
| F4 | Ownership mismatch | `status: ready` carrying an assignee |
| F5 | Stale in-progress | `status: in-progress`, last labeled event >7 days ago, no linked PR and no commits on a matching branch |
| F6 | Review with no doc | `status: in-review` whose justification comment contains no URL |

## Report

One table, most-issues-first isn't necessary — group by finding type:

```
F1  #12  Title...
F1  #19  Title...
F3  #24  Title...  (labeled "status: ready" by turtlemonvh 2026-08-30, no comment since)
...
0 findings on #7, #15, #22 (skip these in output — don't list clean issues)
```

## Guided repair

Never mutate without confirmation. For each finding, propose a fix and
the exact `gh` command(s) via `blanket-issue-triage`'s steps, then ask
before running them — batch the asks (e.g. "here are 8 proposed
`status:` assignments, approve all?") rather than one prompt per issue,
unless something is genuinely ambiguous:

- **F1** — read the issue, propose a status (same rule-of-thumb as
  `blanket-issue-triage`), apply via that skill.
- **F3 on a human-applied label** — ask Timothy why, then post the note
  with `by="human" via="claude-backfill"` — don't relabel.
- **F2** — propose which of the conflicting labels is current and
  correct; remove the other(s), justification comment noting the cleanup.
- **F4** — propose removing the assignee (or, if there's a real reason
  someone should own it, moving the status instead).
- **F5** — ask whether the work is still active; if not, propose
  reverting to `status: ready` (unassigned) or `status: needs-triage` if
  circumstances changed.
- **F6** — ask for the doc link, append it to the existing comment via a
  follow-up comment rather than editing the original.
