---
name: blanket-issue-triage
description: Use when applying or changing a `status:` label on a blanket GitHub issue — the only sanctioned path for that change. Triggers on phrasings like "triage issue N", "mark this ready", "this needs design", "label the new issues", or when another skill/workflow needs an issue moved to a new status.
---

# blanket-issue-triage

Applies label + assignee + justification comment as one atomic step, so
the one-`status:`-label-per-issue invariant holds by construction and no
label ever lands without a reason attached. Full rules are in
[`CONTRIBUTORS.md`](../../../CONTRIBUTORS.md#issue-workflow) — read it
first if this is the first triage action this session, since the label
table, ownership convention, and per-label content requirements live
there and can change independently of this skill.

## Steps

1. **Read the issue.** `gh issue view N --json title,body,labels,assignees,comments`.
2. **Decide the status.** Use this rule of thumb:
   - Acceptance criteria and scope are fully specified, touches code you
     understand, no open questions → `status: ready`.
   - You (or the user) have light open questions that don't require a
     design pass → `status: needs-info`.
   - Real uncertainty about approach, scope, or whether this should be
     decomposed → `status: needs-design`.
   - You can't tell which of the above applies without more context →
     `status: needs-triage`.
   - A design or PR is done and needs a decision → `status: in-review`.
   - A branch/worktree exists and work is underway → `status: in-progress`.
3. **Decide the assignee** per the ownership convention (assignee = whose
   turn). Most of the states above are unassigned (Claude's or nobody's
   turn); `needs-info` and `in-review` are usually assigned to
   `turtlemonvh` unless Claude is the one who owes the next action.
4. **Remove any existing `status:` label**, add the new one, set the
   assignee:
   ```bash
   gh issue edit N --remove-label "status: <old>" --add-label "status: <new>" \
     --add-assignee turtlemonvh   # or --remove-assignee turtlemonvh
   ```
   (Skip `--remove-label` if there wasn't one.)
5. **Post the justification comment**, meeting the per-label content
   requirement from `CONTRIBUTORS.md` (`needs-triage` states what would
   unblock it; `in-review` states the review scope + doc link;
   `needs-info` enumerates the questions):
   ```bash
   gh issue comment N --body "$(cat <<'EOF'
   <!-- blanket-label-note label="status: <new>" by="claude" -->
   **🤖 Claude**

   **`status: <new>`** — <one to three sentences: why this state, what's
   still needed if anything>
   EOF
   )"
   ```

## Backfilling a human-applied label

If the label was applied by Timothy (in the GitHub UI, from his phone,
etc.) without a justification comment, don't relabel — ask him why, then
post the note yourself with the backfill marker:

```bash
gh issue comment N --body "$(cat <<'EOF'
<!-- blanket-label-note label="status: <label>" by="human" via="claude-backfill" -->
**🤖 Claude**

**`status: <label>`** — <Timothy's stated reason, in his words or close to it>
EOF
)"
```

## Batch triage

When triaging many issues at once (e.g. the initial backfill of an
unlabeled repo), do steps 1–5 for each issue and report a compact table
at the end: issue #, title, chosen status, assignee, one-line reason.
Don't ask for per-issue approval mid-batch unless something is
genuinely ambiguous — surface those as `status: needs-triage` with the
open question stated, rather than guessing.
