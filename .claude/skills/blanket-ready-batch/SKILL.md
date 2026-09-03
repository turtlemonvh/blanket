---
name: blanket-ready-batch
description: Use when picking up unassigned blanket issues labeled `status: ready` and running them in parallel — "run the ready issues", "pick up what's clear to execute", "work the backlog". Dispatches up to 3 concurrent subagents in isolated worktrees, one issue each, ending at an opened PR (never auto-merge).
---

# blanket-ready-batch

Turns `status: ready` + unassigned issues into PRs, in parallel, without
auto-merging. Full rules (label states, PR/merge workflow) are in
[`CONTRIBUTORS.md`](../../../CONTRIBUTORS.md#issue-workflow) — read it
first if this is the first batch run this session.

## Steps

1. **Query the batch:**
   ```bash
   gh issue list --state open -l "status: ready" -S "no:assignee" \
     --json number,title,body --limit 20
   ```
2. **Present the batch to the user for approval** before dispatching
   anything — up to 3 at a time, most-actionable-first if there are more
   than 3. Don't dispatch on your own judgment alone; this is real work
   that opens PRs.
3. **Claim each issue** via `blanket-issue-triage`: label ->
   `status: in-progress`, still no assignee (Claude owns it, but Claude
   has no GitHub account — see CONTRIBUTORS.md), justification comment
   noting the batch dispatch.
4. **Dispatch one subagent per issue**, each in its own isolated
   worktree/branch — use `superpowers:using-git-worktrees` for the
   isolation and `superpowers:dispatching-parallel-agents` for running
   up to 3 concurrently. Give each subagent:
   - The issue number, title, and full body.
   - Pointer to `CLAUDE.md`/`CONTRIBUTORS.md` for repo conventions
     (commit style `[AI] ...`, `go fmt`, the three-surface test
     requirement).
   - Instruction to open a PR against `master` when done, referencing
     `Closes #N` in the PR body, and to run the relevant `make docker-*`
     targets before declaring done — don't claim success without running
     them.
5. **On each PR opening**, move that issue to `status: in-review`,
   assign `turtlemonvh`, justification comment linking the PR.
6. **Stop there.** No auto-merge, even on green CI — Timothy reviews and
   approves the PR, then Claude merges after approval. Report the batch
   result: issue #, PR #, one-line summary of what changed, CI status if
   known.

## What this skill does not do

- Decide which issues are `status: ready` in the first place — that's
  `blanket-issue-triage`/`blanket-issue-audit`.
- Merge PRs.
- Order the queue by anything but issue recency — there's no
  `priority:` namespace yet (deliberately deferred; see the design doc
  if you want the reasoning).
