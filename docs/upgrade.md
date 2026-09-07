# Upgrading blanket

How blanket's database is versioned, backed up, and migrated, and what to
do when something goes wrong in the middle of it.

This page covers [issue
#23](https://github.com/turtlemonvh/blanket/issues/23) end to end: the
database's schema versions and backups, the restart state machine, and
`blanket upgrade` / `blanket rollback`, which drive it.

## The short version

```
blanket upgrade --check     # is there a newer release?
blanket upgrade --yes       # install it and restart onto it
blanket rollback --yes      # change your mind
```

`blanket upgrade` verifies the download against the release's
`SHA256SUMS`, keeps the binary it replaces in a [rollback
slot](#rollback-slots), takes a database backup before anything moves, and
walks the running server through the [restart state
machine](#the-restart-state-machine). Everything it does is also
[printable](#print-plan) and doable by hand.

If you are upgrading from a blanket released before 0.3.0:

1. Stop the server.
2. Replace the binary.
3. Start it again.

Your existing database is treated as **schema version 1** and stamped as
such on the first open. Nothing else about it changes — no records are
rewritten, no backup is taken, no migration runs. There are no migrations
to run: blanket ships the machinery with zero migrations in it, so that the
first real schema change is not also the first time the machinery is
exercised.

## Schema versions

The database carries a schema version in a `meta` bucket, alongside four
other facts about the installation rather than the work in it:

| Key | What it holds |
| --- | ------------- |
| `schemaVersion` | The schema the file is written in. Missing means version 1. |
| `serverInstance` | Which server process last owned this database, and when it started — the same `instanceId` a worker's heartbeat response carries. |
| `lockHolderPid` | The process holding the database's exclusive lock. Written on open, cleared on a clean close, so a record left behind means the previous blanket crashed. |
| `migrationMarker` | Set while a migration is in flight; see below. |
| `restartRecord` | What the restart state machine is doing, and what the next process owes the workers. Absent when nothing is in flight. See [the restart state machine](#the-restart-state-machine). |

The `meta` bucket is created on **every** open if it is missing, so an
older database becomes a current one simply by being opened. A database
with no `schemaVersion` key is read as version 1: that is a definition, not
a guess — the pre-`meta` layout *is* version 1, since nothing had ever
changed it.

### Which lock-holder record you can actually read

There are two copies of the lock-holder record and they exist for different
readers. The one in `meta` is transactional and durable. A process that has
been locked *out*, though, cannot read the database at all — BoltDB takes
an exclusive file lock and even a read-only open needs a shared one. So the
same record is also written to a sidecar file next to the database,
`blanket.db.lock.json`, which is what turns

```
could not acquire lock on bolt database "blanket.db" after 5s: is another blanket process already running?
```

into

```
could not acquire lock on bolt database "blanket.db" after 5s: pid 4213 on hostname has it open (since 2026-09-06T21:02:11Z)
```

The sidecar is a hint, not an authority: a recorded pid that is
conclusively dead is reported as a stale lock rather than as a live holder,
so the message never sends you off to kill an innocent process that
happened to inherit the pid.

## Migrations

Migrations are **forward-only** and numbered. There are no `Down()`
functions and there will not be: an inverse migration is code that is
written once, run approximately never, and therefore tested by nobody —
broken exactly when you finally need it. Going back means putting a file
back, which is what `--restore` does.

Two properties make this safe:

- **A backup is mandatory before any migration.** If the backup cannot be
  taken, the migration does not run.
- **Each migration's data change and its version bump commit in one
  transaction.** A crash mid-migration leaves either the change applied and
  the version bumped, or neither — never a half-migrated file that claims
  to be the old version.

### When they run

Automatically, when the server opens the database. Not on demand, and not
after a prompt.

That is deliberate, and it is the one place this design gives up a safety
rail for a bigger one. Under `Restart=always` — the systemd unit blanket's
installer writes — a server that refused to start until a human ran
`blanket migrate` would crash-loop, and the crash-looping server and the
human's `migrate` command would then race each other for the database lock
on a few-second timeout. That is a livelock built into the recovery path.
Auto-applying forward, after a backup, avoids it.

### Version mismatch

Let *db* be the version stamped in the file and *target* the highest
version the binary knows.

| Situation | What happens |
| --------- | ------------ |
| db = target | Normal open. |
| db < target | Back up, apply each pending migration, clear the marker. Logged at warn level. |
| db > target | **Refuses to start.** The database was written by a newer blanket, and migrations are forward-only, so this binary has nothing useful it could do. The message names both versions. Install the newer blanket, or restore a backup taken before the upgrade. |

### The migration marker

A `migrationMarker` is written before the first migration transaction and
cleared after the last one, recording `{from, to, startedTs, backupPath}`.
Finding one set on boot means a process died in between. What happens next
depends on who is booting:

| Marker state | What the booting binary does |
| ------------ | ---------------------------- |
| `to` is higher than this binary's target | A **newer** binary is mid-migration. Logs `migration v3→v5 in progress; this v3 binary is exiting to await the binary swap` and exits non-zero, touching nothing. It does **not** retry — two binaries racing for the lock forever is not a recovery. |
| `to` is at or below this binary's target, and the stored version has already reached `to` | The migration transaction committed; only the write that clears the marker was lost. Harmless. The marker is cleared and boot continues. |
| `to` is at or below this binary's target, and the version has not reached it | A migration **died mid-flight**. Boot is refused, and the error names the exact restore command. |

That last case refuses rather than restoring automatically, on purpose. An
automatic restore silently discards whatever happened between the backup
and the crash, and the code has no way to know whether that mattered. Worse,
under a supervisor an automatic restore that itself fails becomes a loop
that rewrites the database on every boot. Refusing stops the world exactly
once, somewhere a human can see it.

The error looks like:

```
a migration v1→v2 started at 2026-09-06T21:02:11Z never finished; the database is still at version 1.
Restore the pre-migration backup before starting blanket again:

    blanket migrate --restore /var/lib/blanket/backups/blanket-1-2026-09-06T21-02-11.418Z.db --yes
```

## Backups

### Where they go, and what they are called

`<the directory holding your database>/backups/`, overridable with the
`storage.backupDir` config key. Beside the database rather than somewhere
else, because the property that matters when you are restoring at 2am is
that the backup is where you will look for it.

Filenames are `blanket-<schemaVersion>-<timestamp>.db`, e.g.
`blanket-1-2026-09-06T21-02-11.418Z.db`. The schema version is in the name
because it is the fact you need in order to choose a backup, and reading it
out of the file means opening the file. The timestamp is RFC3339 with `:`
replaced by `-` (Windows filenames cannot contain a colon) and keeps
milliseconds, so two backups taken in the same second do not overwrite each
other.

### How the copy is taken

Through BoltDB's `tx.WriteTo` inside a read transaction. BoltDB's MVCC
gives that transaction a consistent point-in-time image of the whole file,
so the copy is valid **while the server is serving** — no pause, no
downtime, no "stop the server first". A `cp` of a live database gives none
of those guarantees.

### The free-space precheck

A backup momentarily doubles the database's footprint, so:

| Free space | Behaviour |
| ---------- | --------- |
| less than the database size | **Refused.** Writing would fail part-way and leave a truncated file that looks like a backup. |
| less than 4× the database size | Proceeds, with a warning. Retention keeps 3 backups plus the live database, so below 4× the retention policy cannot be sustained. |
| unknown (a platform with no `statfs`) | Proceeds, and says so. "Don't know" is not "no space", and a backup is mandatory before a migration. |

### Retention

The **3 newest** backups are kept and older ones are pruned; the count is
`storage.backupRetention`. Three matches the three [rollback
slots](#rollback-slots) `blanket upgrade` keeps — the database half of a
slot is exactly one of these files.
There is no size cap, by design: a cap that silently stopped taking
backups would defeat the purpose, so the policy is a count plus a warning.

Pruning happens *after* a new backup is written, never before. A prune that
ran first would, in the disk-full case this code exists to survive, delete
a good backup and then fail to write its replacement.

Files that do not match the `blanket-*.db` naming are never touched. The
backups directory is yours too.

## Commands

### `blanket backup`

Writes a consistent copy of the database. Works whether or not the server
is running, and picks its own route:

- **Server up** → `POST /ops/backup`, and the server takes the backup. The
  CLI cannot open the database while the server holds its lock, so this is
  the only way to back up a live install.
- **Server down** → the CLI opens the database directly.

It tries the server first. The other order would take the lock and thereby
stop a running server from serving, which is a spectacular way to fail an
operation whose entire purpose is safety. A server that answers and
*refuses* is reported as a refusal — the CLI does not then go behind its
back.

```
blanket backup                 # to <database dir>/backups/
blanket backup --dir /mnt/nfs  # somewhere else
```

### `blanket migrate`

```
blanket migrate --check                   # report; change nothing
blanket migrate                           # back up, then migrate
blanket migrate --restore PATH --yes      # replace the database with a backup
```

All three need the server **stopped**: only one process can hold the
database lock at a time.

`--check` exits **0** when nothing is pending, **1** when something is
(pending migrations, a stale marker, a too-new database), and **2** when it
could not tell — the database is locked, missing, or unreadable. Scripts
can branch on that without parsing prose. It opens the database read-only
and writes nothing, not even the `meta` bucket.

```
$ blanket migrate --check
database:       /var/lib/blanket/blanket.db
stamped:        yes
schema version: 1
target version: 1

Up to date; nothing pending.
```

`blanket migrate` with no flags runs the *same* code path the server runs
at open — not a second implementation of it. A CLI with its own copy of the
migration sequence is a CLI that will eventually disagree with the server
about what a database needs, and the disagreement would surface as data
loss rather than as a compile error.

`--restore` makes three checks before it replaces anything:

1. **The database must not be in use.** It proves that by taking the lock
   itself; nothing else is a real check. Overwriting the file underneath a
   running server does not restore anything — BoltDB has the old pages
   mapped and flushes them back over the new contents on the next write.
2. **The backup is verified first**, while the live database is still the
   only thing anyone is relying on. Discovering half-way through a restore
   that the "backup" is a truncated file would mean the other copy had
   already been destroyed.
3. **What it replaces is kept**, renamed to
   `<database>.pre-restore-<timestamp>` rather than deleted. Restoring the
   wrong backup is an easy mistake at 2am, and it should be an undoable one.

`--yes` is required, and there is no interactive prompt: this runs in
maintenance windows and from scripts, where a prompt is a hang.

## Upgrading with the CLI

`blanket upgrade` is a *driver* of everything else on this page. It does
not contain its own copy of the backup, the migration or the restart — the
server owns all three — and what it adds is the part that lives outside the
process: choosing a release, proving the bytes are the right bytes, putting
the file on disk without ever leaving a partial one there, keeping what it
replaced, and writing down what it did so a `kill -9` of the CLI is
recoverable.

```
blanket upgrade --check                    is a newer version available?
blanket upgrade --print-plan               the exact steps, with real paths
blanket upgrade --yes                      do it
blanket upgrade v0.5.0 --yes               a specific version
blanket upgrade --bundle b.tar.gz --yes    from an offline bundle
blanket upgrade --stage-only               download + verify, install nothing
blanket upgrade --no-restart --yes         install, leave the server alone
blanket upgrade --resume --yes             finish an interrupted attempt
blanket upgrade --abort                    unwind an interrupted attempt
```

### The sequence

```
discover   which version, and where its bytes come from
verify     against SHA256SUMS, always
stage      a temp file beside the installed binary
begin      POST /ops/restart/begin
backup     POST /ops/backup            -> BACKED_UP, and the record names the file
pause      POST /ops/restart/pause     -> no worker is forked from here on
slot+swap  keep the old binary, rename the new one into place
swapped    POST /ops/restart/swapped
drain      POST /ops/restart/drain     -> unless --drain-mode never
exec       POST /ops/restart/exec
verify     a server answers with a NEW instanceId and the new version
```

Every step is written to [the journal](#the-upgrade-journal) before the
next one starts.

An upgrade **drains** where a routine restart does not. That is
[`--drain-mode`](#--drain-mode-whether-a-restart-stops-its-workers)'s whole
distinction: a worker rides out a server restart by design, so a config
change should not stop the fleet — but a new binary contains new *worker*
code, and a worker that survived the restart is running the old one.
`--drain-mode never` is how an operator who would rather keep a long task
running says so.

### Verification, and why there is no `--no-verify`

Every binary is checked against the release's `SHA256SUMS` asset before it
is installed, and there is no flag to turn that off. A flag that disabled
the only integrity check on a file about to replace the one you are running
is a flag that every "just make it work" answer on the internet would tell
people to pass.

The consequence is worth stating plainly:

> **Releases published before this feature existed carry no `SHA256SUMS`
> and cannot be auto-upgraded *to*.** There is no honest way to add
> checksums to a published release after the fact. `blanket upgrade
> v0.2.0` will refuse and say so. Install such a release by hand, or build
> a [bundle](#offline-bundles) from it.

Upgrading *from* an old release is unaffected — what matters is which
release you are upgrading to.

### What "a server came back" has to mean

The last step verifies the *restart*, and it takes two facts to do it: the
server answering on the port reports an `instanceId` different from the one
the CLI read before it started, **and** its version banner names the
version that was installed. Neither half is enough alone.

- The instance id alone would accept the process being replaced. The old
  server keeps serving right up until its listener closes, so a check that
  took the first `200` it saw would routinely "verify" the process it was
  supposed to have replaced.
- The version alone would accept a server that never restarted at all —
  the case where the swap landed but the exec did not.

And a *third* process can satisfy the first half on its own: an abandoned
blanket parked on the [database lock](#config-keys), waiting for a
port it should never get. It has an instance id of its own, and it wins the
port the moment the real server lets go of it — but it is running the old
binary, and the version is what catches it. The wait keeps waiting when
something on the wrong version answers, for as long as its 90-second
budget lasts; only if that runs out does it report the version it saw.

The CLI is careful not to create such a process itself. When it has to
start the replacement (`--exec-mode=exit` on an unsupervised box), it does
so only if the port is quiet **and stays quiet** for a few seconds — a
server re-execing in place stops answering for a moment, and one refused
connection looks exactly like a server that exited for good. Symmetrically,
"is there a server here at all?" is asked with a short retry rather than
once, so an install is never read as *stopped* just because it was between
process images at that instant.

If the instance id could not be read before the restart — the server was
between processes then too — the CLI says so in a warning and verifies on
the version alone, which is the only evidence there is.

### Exit codes

Five outcomes are distinguishable rather than collapsed into 0/1, because
the caller is usually a script in a maintenance window and the difference
between "there was nothing to do" and "I replaced the binary but the server
did not come back" is the difference between going to bed and getting
paged. `blanket rollback` uses the same codes.

| Code | Meaning |
| ---- | ------- |
| `0` | Done: the new binary is installed and a new server answered on it. For `--check`, **an upgrade is available**. |
| `10` | **Nothing to do** — already at the target version, or nothing to roll back to. |
| `11` | **Staged only** — the binary is staged or installed and no restart was attempted (`--stage-only`, `--no-restart`, or no server was running). |
| `12` | **Verification failed** — a checksum did not match, or the server that came back is not the one that was installed. |
| `13` | **Restart refused** — the binary is in place but the server would not restart: a 409 from the state machine, or a restart already in flight. |
| `1` | Any other error. |
| `2` | Usage: the flags don't make sense, or a precondition a human must fix is unmet (including a missing `--yes`). |

Note that `--check` exits **0** when there *is* something to do, which is
the opposite of [`blanket migrate --check`](#blanket-migrate). They answer
different questions: `migrate --check` is a health probe ("is anything
pending?", where pending is the abnormal case), and `upgrade --check` asks
"would this command do something?".

### `--print-plan`

Prints the [manual `curl` sequence](#restarting-by-hand) with *this*
install's port, paths, slot directory and target version substituted in,
and stops. It is the same seven steps in the same order, because it is the
same sequence — the command runs it over HTTP instead of through `curl`.

```
$ blanket upgrade --print-plan
# blanket upgrade v0.4.0 -> v0.5.0
# The CLI runs exactly these steps over HTTP. Journal: /var/lib/blanket/upgrade/journal.json

BASE=http://localhost:8773
OPS=(-H 'X-Blanket-Restart: 1')

# 0. Verify and stage the new binary beside the installed one.
#    source: https://github.com/turtlemonvh/blanket/releases/download/v0.5.0/blanket-linux-amd64
#    checksums: SHA256SUMS from the v0.5.0 release
#    staged as: .blanket-upgrade-XXXXXX* in /home/you/.local/bin
...
```

### Atomic staging

The rule: **the installed path is never a partial file.** The download goes
to a temp file *in the same directory* as the installed binary (a different
directory can be a different filesystem, which would make the last step a
copy rather than a rename), is verified there, and is then renamed into
place. Rename within a directory is atomic — any reader sees either the old
file or the new one.

On unix that rename is the whole swap: the kernel keeps the old inode alive
for the running process, so the server keeps executing the image it started
with until it re-execs. On **Windows** it cannot be, because Windows
refuses to replace a file that is currently executing. There the running
`blanket.exe` is renamed out of the way first (`blanket.exe.old-<ts>`) and
swept on the next `blanket upgrade` or `blanket rollback`, once the process
holding it is gone.

`scripts/install.sh` and `scripts/install.ps1` follow the same
download-verify-rename pattern, for the same reason.

### Rollback slots

Each upgrade keeps the binary it replaced, together with the name of the
database backup taken just before it, in a **slot** under
`<state dir>/slots/<timestamp>-<version>/`. Three are kept and older ones
are pruned (`upgrade.slots`), matching `storage.backupRetention` — the
database half of a slot *is* one of those backup files, so a fourth slot
would be one whose backup had already been pruned out from under it.

There is no size cap, by design (the same call the backups make): a cap
that silently stopped keeping slots would remove the safety net in exactly
the situation it exists for. Instead, an upgrade **warns** when the
filesystem holding the slots has less room than three more copies of the
binary would need.

Pruning happens *after* the new slot is written, never before. A prune that
ran first would, on a full disk, delete a good rollback point and then fail
to create its replacement.

```
blanket rollback --list               what can be rolled back to
blanket rollback --yes                the newest slot
blanket rollback --slot <dir> --yes   a specific one
blanket rollback --restore-db --yes   also restore that slot's database backup
```

The saved binary is verified against the digest recorded when it was saved,
before it is put back — a rollback that installed a corrupted binary would
turn a bad upgrade into an unbootable install. A rollback also keeps a slot
of what *it* replaced, so it is itself undoable.

**`--restore-db` is opt-in and the binary is not**, because they are not
the same kind of undo. Putting the binary back loses nothing. Putting the
database back discards every task, worker record and queue entry created
since the backup was taken. It also requires the **server stopped**, for
the same reason `blanket migrate --restore` does: overwriting the file
underneath a running server does not restore anything, since bolt has the
old pages mapped and flushes them back over the new contents on the next
write.

### The upgrade journal

The CLI's own state, at `<state dir>/journal.json`. This is the half of the
restart state that [lives outside the
database](#why-the-state-lives-in-two-places), and it exists because the
facts `--resume`, `--abort` and `blanket rollback` need — where the staged
binary is, what the previous binary was, which backup was taken — must
survive precisely the event that erases the server's record: the next boot.

One JSON object, rewritten in full on every transition via a temp file plus
rename (a torn journal is worse than none, because a half-written one still
parses often enough to be believed). Fields are additive-only and unknown
ones are ignored on read, since reading the journal is exactly what a
downgrade does.

```json
{
  "schema": 1,
  "id": "6a9e386e6be34ccbb1ba428f",
  "action": "upgrade",
  "state": "VERIFIED",
  "fromVersion": "v0.4.0",
  "toVersion": "v0.5.0",
  "binaryPath": "/home/you/.local/bin/blanket",
  "stagedPath": "",
  "sha256": "7edc89b2…",
  "slotPath": "/var/lib/blanket/upgrade/slots/20260907040709.183-v0.4.0",
  "backupPath": "/var/lib/blanket/backups/blanket-1-2026-09-07T04-07-10.293Z.db",
  "source": "github",
  "port": 8773,
  "restartId": "…",
  "fromInstanceId": "…",
  "toInstanceId": "…",
  "steps": [{ "state": "PLANNED", "ts": 1788754030 }, "…"]
}
```

`state` is one of `PLANNED`, `STAGED`, `BACKED_UP`, `PAUSED`, `SWAPPED`,
`DRAINED`, `EXECED`, `VERIFIED`, `ABORTED`, `FAILED`. The first eight
mirror the server's states where the two coincide, so a journal and a `GET
/ops/restart/status` read as the same story; `PLANNED` and `STAGED` are the
driver's alone, because downloading a file is not something the server ever
sees.

### `--resume` and `--abort`

| Journal state when the CLI died | `--resume --yes` | `--abort` |
| ------------------------------- | ---------------- | --------- |
| `PLANNED` | Refuses. A partial download cannot be resumed — the only safe thing to do with an unverified file is throw it away. Run the upgrade again. | Removes the staging file. |
| `STAGED` | Re-verifies the staged binary against the recorded digest, then continues from `begin`. | Removes it; nothing was installed. |
| `BACKED_UP`, `PAUSED` | Continues, skipping the transitions the server has already made. | Clears the server-side restart (un-pausing worker spawn); nothing was installed. |
| `SWAPPED`, `DRAINED`, `EXECED` | The new binary is already installed; finishes the restart. If a server answers, its own `resolvedExecMode` decides whether the CLI has to start the replacement or the server is bringing itself back. | Clears the server-side restart and **leaves the new binary in place**, naming `blanket rollback` as the way back. |

`--abort` always tries `POST /ops/restart/abort` first, journal or no
journal: a paused server is the failure mode that outlives everything else,
and it is the one an operator typing `--abort` most needs undone.

Forward-only means "do the transitions that have not happened yet", which
is both the first-run path and the resume path — the CLI reads the server's
current state and skips ahead, rather than having a second implementation
for resuming. A restart in flight that is *not* this attempt (matched by
id) is refused, because two drivers racing over one state machine is how an
install ends up paused with nobody to un-pause it.

### Offline bundles

A **bundle** is one file containing everything an install needs and nothing
it has to fetch: the binaries for all three platforms, the `SHA256SUMS`
that covers them, the example task types, the `blanket-task-type` skill,
the install scripts, and a `manifest.json` naming the version. Releases
attach one as `blanket-bundle-<version>.tar.gz`; `make bundle` builds one
locally.

```
blanket upgrade --bundle blanket-bundle-v0.5.0.tar.gz --yes
```

`--bundle` accepts either the tarball or an already-extracted directory —
an air-gapped operator ends up with the latter after unpacking once onto a
share, and refusing it would mean re-tarring a directory to satisfy a
format check.

**Verification is identical to the online path.** The bundle is not trusted
for being local: it carries the same `SHA256SUMS`, generated by the same
script, and the binary is checked against it before it is installed. What
the bundle changes is where the bytes come from, not whether they are
checked. It is also what
[docs/offline_install.md](offline_install.md) now points at, in place of a
four-step "download these things separately from the same tag, and be
careful they match" checklist — which is a checklist a human executes, and
therefore a checklist a human gets wrong.

### The update notice

`blanket ps` prints one line when a newer release is known:

```
A newer blanket is available: v0.5.0 (you have v0.4.0). Run `blanket upgrade` to install it.
```

The requirement here is a constraint, not a feature: **this can never slow
the CLI down.** `blanket ps` is a command people run in a loop and pipe
into other things, and a version check that added a network round trip to
it would be a regression dressed as a courtesy.

So: the message comes from a cache an *earlier* run wrote. A cold cache
prints nothing rather than fetching. The refresh runs at most once a day,
in a background goroutine with a hard 250ms budget, and writes the cache
for the next invocation — so an unreachable GitHub and a firewall that
blackholes packets cost exactly the same, because the budget is a deadline
and not a timeout on a response. The check-stamp is written even when the
fetch fails, or an offline machine would retry on every single invocation
forever.

It stays quiet when stdout is not a terminal (so `blanket ps | wc -l` is
unaffected), under `--json`, and entirely when
`upgrade.checkForUpdates: false`.

### Windows

Windows never self-restarts. A detached replacement escapes a service's job
object, so `sc stop blanket` cannot reach it and it holds the database lock
invisibly — the worst failure mode in the system. So `--exec-mode` degrades
to `exit` there, and after the old server stops the upgrade CLI **starts
the replacement itself**. Under the service control manager it prints the
command instead:

```powershell
sc start blanket
```

## The restart state machine

Replacing a running blanket is a sequence, and the thing driving it is
**outside the process**: `blanket upgrade`, or `curl`. Any step
can be the last one, because a machine can lose power between any two of
them. So the sequence is written down, one state at a time, every step is
an HTTP call, and every state has a documented recovery.

### Why the state lives in two places

The driver cannot open the database. BoltDB's lock belongs to the server
for as long as the server is up, and the interesting part of a restart is
exactly the window where the server is going away and coming back. So:

- the **restart record**, in the database's `meta` bucket, is what the
  *next server process* reads on boot to find out what it is the
  continuation of;
- a **journal file** owned by the driver is what a *human* reads when the
  server is down and did not come back.

Nothing in the server reads the journal — the server learns everything it
needs from the record plus the worker records — so its format belongs to
the CLI that writes it. It is documented under [the upgrade
journal](#the-upgrade-journal).

### The states

| State | Set by | What it means | Server behaviour |
| ----- | ------ | ------------- | ---------------- |
| `IDLE` | the absence of a record | Nothing in flight. | Normal. |
| `STAGED` | `POST /ops/restart/begin` | A restart has been announced. Nothing has changed. | The [reaper](task_flow.md#the-reaper) stands down; the deadline watchdog is armed. |
| `BACKED_UP` | `POST /ops/backup` while a record is at `STAGED` | A backup exists, and the record names it. | As `STAGED`. |
| `PAUSED` | `POST /ops/restart/pause` | Worker spawn is refused with 409. | + no new workers. |
| `SWAPPED` | `POST /ops/restart/swapped` | The caller reports the binary on disk is the new one. The only state the server cannot observe for itself. | As `PAUSED`. |
| `DRAINING` | `POST /ops/restart/drain` | Every running worker has been stopped with a respawn intent — in **one transaction** with this state change. | + workers stopping. |
| `EXECING` | `POST /ops/restart/exec` | The shutdown has begun. | Going away. |
| `VERIFIED` | the next process's boot | The replacement found the record, brought back what the drain stopped, and cleared it. | Logged on the way back to `IDLE`; never left in the file. |

Transitions are **forward-only**, and skipping ahead is the normal case: a
routine restart takes no backup, swaps no binary and drains nothing —
`begin` then `exec` is a complete, valid sequence. Going backwards, or
repeating a state, is a 409. `abort` goes to `IDLE` from anywhere.

### The one invariant

Every transition is a single database transaction, and **two facts that
must agree are never written in two of them**. The case that matters is the
drain: "this restart is at `DRAINING`" and "these six workers are stopped
and are to be brought back" commit together. Split in two, a crash between
them would leave either six workers stopped that nothing will ever restart,
or a promise to restart six workers that are still claiming tasks — and
nothing on disk would say which.

### What happens if the machine dies mid-restart

Whatever state it was in, **the next server clears the record**. A boot is
the strongest evidence obtainable that the process which wrote it is gone:
it held the database lock, and this one has it. Keeping the record — staying
paused because somebody paused you before the power went out — would leave
an install unable to start a worker, permanently, with the only remedy
being the restart that just failed.

Clearing the record is not forgetting. The part that must survive is the
obligation to *specific workers*, and that lives on the worker records as
respawn intent, which the boot deliberately does not touch. The record is
the plan; the intents are the debt.

| Died at | What the next server does | What you do |
| ------- | ------------------------- | ----------- |
| `STAGED` | Clears the record. Nothing had changed. | Start again. |
| `BACKED_UP` | Clears the record. The backup is still on disk, in `backups/`. | Start again; you can reuse the backup. |
| `PAUSED` | Clears the record, so spawn works again. | Start again. |
| `SWAPPED` | Clears the record. **The binary on disk may be the new one and the running server the old one** — but that is now simply "a server running an old image", which the next restart fixes. | Start again; it will be a very short one. |
| `DRAINING` | Clears the record, then respawns every worker carrying an intent. | Nothing, unless a worker is missing — check its `stoppedReason`. |
| `EXECING` | Same as `DRAINING`. The exec never happened; the debt to the workers did. | Nothing. |

`scripts/restart_machine.sh` asserts every row of that table against a real
process, by killing the server at each state in turn
(`BLANKET_TEST_CRASH_AT`) and checking the replacement's behaviour.

### Worker respawn, and the three storm guards

A drain records a respawn intent on each worker it stops. The intent is
cleared **after** a successful spawn, not before, which makes respawn
at-least-once: losing a worker on an upgrade is a silent, lasting failure,
while spawning one twice is loud and self-correcting. Three guards keep
"at least once" from becoming "forever":

1. **Pid liveness.** Before spawning, the server checks whether the worker
   is already running (its pid paired with that pid's start time, so a
   recycled pid cannot masquerade). If it is — the normal outcome when the
   previous process died after forking but before clearing the intent — the
   intent is settled by observation rather than by a second fork.
2. **A generation cap.** Three attempts. Past that the intent is dropped
   and the worker left stopped with `stoppedReason: "restart: gave up
   respawning after N attempts"`. The attempt is counted *before* the fork,
   so the cap converges even when the spawn is what kills the server.
3. **A minimum interval.** 30s between two respawns of the same worker.
   This is what stops a server crash-looping under a supervisor from
   forking the whole fleet on every boot.

### The deadline watchdog

The driver is a separate process and can be `kill -9`ed. The record
therefore carries a deadline, **every transition refreshes it** (it is a
watchdog, not a budget: a driver still calling in is not the one this
exists to notice), and a loop inside the server aborts the restart when it
lapses — lifting the pause and bringing back anything the drain stopped.
That is the same code `POST /ops/restart/abort` runs, so the recovery a
human triggers is the one every timeout has already exercised.

### Restarting by hand

The full sequence, in the order `blanket upgrade` will run it. Every call
needs the `X-Blanket-Restart` header and must come from loopback; see
[the ops endpoints](#the-ops-endpoints).

```bash
BASE=http://localhost:8773
OPS=(-H 'X-Blanket-Restart: 1')

# 0. Where are we? (IDLE, unless a previous attempt is still open.)
curl -sS "${OPS[@]}" $BASE/ops/restart/status

# 1. Announce it. Nothing is paused or stopped yet.
curl -sS "${OPS[@]}" -H 'Content-Type: application/json' \
     -d '{"reason": "upgrade to 0.4.0"}' \
     -X POST $BASE/ops/restart/begin

# 2. Back up, while the server is still serving. This also advances the
#    record to BACKED_UP and records the path.
curl -sS "${OPS[@]}" -X POST $BASE/ops/backup

# 3. Stop the server spawning workers, so none is forked from a binary
#    that is about to be replaced. From here, POST /worker/ answers 409.
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/pause

# 4. Swap the binary. Not an API call — this is you, or your package
#    manager. Then tell the server it happened.
install -m 0755 ./blanket-new "$(command -v blanket)"
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/swapped

# 5. Drain — ONLY if the new binary changes worker behaviour. A routine
#    restart skips this: a worker rides out a server restart by design.
#    Waits up to restart.drainTimeout; answers `drained: false` with a
#    list if something is still running.
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/drain

# 6. Go. 202, then the server tears down and either re-execs in place or
#    exits 75 for its supervisor — see --exec-mode below.
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/exec

# 7. Verify. A different instanceId means a different process came back.
until curl -fsS $BASE/version >/dev/null 2>&1; do sleep 0.5; done
curl -sS "${OPS[@]}" $BASE/ops/restart/status   # -> IDLE
curl -sS $BASE/config/ | grep instanceId
```

Changed your mind at any point before step 6:

```bash
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/abort
```

### `--exec-mode`: how the server is replaced

| Value | What `exec` does |
| ----- | ---------------- |
| `auto` (default) | Under a supervisor, **exit**; unsupervised, **re-exec in place**. |
| `exec` | Always re-exec in place: `syscall.Exec` over the binary at `os.Executable()`, same argv, same environment. The pid is preserved, so a server started under `nohup` or in tmux keeps its terminal and its logs. |
| `exit` | Always drain and exit **75**, leaving the restart to whatever started this process. |

A supervised process that re-execs itself is invisible to the thing that is
supposed to be managing it, and escapes whatever that thing would have done
about a bad new binary. That is why `auto` exits under one.

"Supervised" is a **heuristic**: systemd's `INVOCATION_ID` / `JOURNAL_STREAM`
/ `NOTIFY_SOCKET` / `LISTEN_PID`, or launchd's `XPC_SERVICE_NAME`. Set
`BLANKET_SUPERVISED=1` (or `=0`) for a deployment it cannot recognise, or
just say what you mean with `--exec-mode`. `GET /ops/restart/status` reports
both the configured mode and the resolved one.

The exit code is **75** (`EX_TEMPFAIL`) and not 0, which is not a detail:
the systemd unit blanket's installer writes says `Restart=on-failure`, so a
clean exit is precisely the one thing a supervised server must not do when
it wants to come back.

**Windows never self-restarts.** A detached replacement escapes a service's
job object, so `sc stop blanket` cannot reach it and it holds the database
lock invisibly — the worst failure mode in the system. There, `exec`
degrades to `exit` and you (or the upgrade CLI) start the server again:

```powershell
sc start blanket    # or: blanket
```

### `--drain-mode`: whether a restart stops its workers

| Value | Behaviour |
| ----- | --------- |
| `auto` (default) | Drain only when the caller asks, by calling `POST /ops/restart/drain`. |
| `always` | `exec` drains first, whether or not the caller asked. |
| `never` | `POST /ops/restart/drain` is refused with 409. |

Routine restarts do not drain, on purpose: a worker rides out a server
outage by design (it retries with backoff and keeps its task running), so
stopping the fleet for a config change would cost real work for nothing.
Drain when the *worker* code changes — an upgrade — which is what
`blanket upgrade` does, and what `always` makes unconditional for an
install that would rather be certain than quick.

## Config keys

| Key | Default | What it does |
| --- | ------- | ------------ |
| `storage.openTimeout` | `5s` | How long to wait for the database's exclusive lock before giving up. It was 1s before, which is shorter than a normal shutdown — under `Restart=always` the supervisor starts the replacement immediately, and a budget shorter than the old process's drain-and-teardown turns a routine restart into a crash loop. |
| `storage.backupDir` | `""` | Where backups go. Empty means `<database dir>/backups`. |
| `storage.backupRetention` | `3` | How many backups to keep. |
| `restart.execMode` | `auto` | How a requested restart replaces the process: `auto`, `exec`, `exit`. Also `--exec-mode`. |
| `restart.drainMode` | `auto` | Whether a restart stops its workers: `auto`, `always`, `never`. Also `--drain-mode`. |
| `restart.drainTimeout` | `60s` | How long a drain waits for stopped workers to exit before reporting the stragglers. A bound on the *wait*, never on the task — a worker finishes what it is running first. Also `--drain-timeout`. |
| `restart.deadline` | `5m` | How long the watchdog gives the restart's driver between transitions before aborting. Generous: the steps it spans include a human swapping a binary by hand. |
| `upgrade.checkForUpdates` | `true` | Whether the CLI prints [the update notice](#the-update-notice). Off is one line for an install that would rather its CLI never mentioned the internet. |
| `upgrade.stateDir` | `""` | Where the journal, the rollback slots and the notice cache live. Empty means `<database dir>/upgrade`, beside the backups the slots pair with. |
| `upgrade.slots` | `3` | How many [rollback slots](#rollback-slots) to keep. |
| `upgrade.repo` | `turtlemonvh/blanket` | Which repository releases come from. |
| `upgrade.releasesBaseURL` | `https://api.github.com` | The Releases API root. Overridable (and `--releases-base-url`, hidden) so `scripts/upgrade.sh` can serve a fake releases API off localhost; a suite that reached api.github.com would fail whenever an unauthenticated CI runner got rate-limited. |

## The ops endpoints

`POST /ops/backup` and the seven `/ops/restart/*` routes are a small group
of privileged, local-only endpoints. All of them are:

1. **loopback only**, decided from the socket's own address and never from
   `X-Forwarded-For`, which the caller writes and could simply lie in;
2. **required to carry an `X-Blanket-Restart` header**, whose job is not to
   be a secret but to force a browser into a CORS preflight;
3. **carved out of blanket's wildcard CORS policy**, so that preflight is
   refused.

blanket has no authentication and allows all origins, which is a tolerable
posture for endpoints that list tasks on a single-user box and not at all
tolerable for endpoints that write files and restart the server. The three
layers together mean a web page you happen to visit cannot drive them.
`curl`, a script, or the CLI is unaffected — they set the header and
connect from loopback, which is what a local operator is.

There is deliberately no token yet. Loopback-only covers the
single-machine install, which is the only shape that exists today; a token
is additive when someone needs to drive an upgrade from off-box.

See [api.md](api.md#ops-endpoints) for the request and response shapes.

## Troubleshooting

**"could not acquire lock … pid N has it open"** — another blanket has the
database. Stop it. If the named pid is reported as gone, the lock file is
stale and will clear once the old process's file handle is released.

**"database schema version N is newer than this binary understands"** — you
have downgraded. Install the newer blanket back, or restore a backup taken
before the upgrade.

**"a migration vX→vY … never finished"** — run the `blanket migrate
--restore … --yes` command the message prints, then start the server.

**"migration vX→vY in progress; this vX binary is exiting"** — an older
binary is starting while a newer one is mid-migration. Finish the binary
swap; nothing is wrong with the database.

**"a server restart is in flight; worker spawn is paused"** (409 on
`POST /worker/`) — somebody called `POST /ops/restart/pause` and has not
finished. `GET /ops/restart/status` says who and when; `POST
/ops/restart/abort` ends it. It also ends itself, after
`restart.deadline`, and it never survives a restart of the server.

**"this release publishes no SHA256SUMS"** — you asked to upgrade to a
release cut before checksums were published. It cannot be verified and so
will not be installed; see [the SHA256SUMS
caveat](#verification-and-why-there-is-no---no-verify). Install it by hand,
or use `--bundle`.

**"checksum mismatch"** — the bytes that arrived are not the bytes the
release says they are. Nothing was installed and the staged file was
removed. Retry; if it repeats, download the asset and the `SHA256SUMS` by
hand and compare them yourself before going any further.

**"a restart is already in flight"** — another driver (or a previous run of
this one that is not this attempt) has the state machine. `blanket upgrade
--abort` clears it, as does `POST /ops/restart/abort`; it also clears
itself after `restart.deadline`.

**"a server came back but reports <the old version>"** — something is
serving the port that is neither the process that was replaced nor the one
that was installed. The usual culprit is a second blanket that was started
against this install and has been sitting on the database lock: check for
another process running the same binary (`ps` for the installed path,
`<state dir>/server.log` if the CLI started one), stop it, and run the
command again. `blanket rollback --yes` puts the previous binary back if
the install itself is what is wrong.

**The upgrade exited 12 and the server did not come back** — the new binary
*is* installed. Start it by hand to see what it says, or `blanket rollback
--yes` to put the previous one back. `<state dir>/journal.json` records
exactly how far the attempt got.

**A `blanket.exe.old-*` file next to the binary (Windows)** — the running
`.exe` that a swap moved aside. It is removed by the next `blanket upgrade`
or `blanket rollback`, once the process holding it has exited.

**A worker did not come back after an upgrade** — check its
`stoppedReason`. `restart: gave up respawning after 3 attempts` means the
server tried and the worker would not start; its own logfile
(`GET /worker/:id/logs`) says why. An empty reason with `stopped: true`
and no `respawnIntent` means somebody stopped it explicitly during the
restart, which is a decision that outranks the respawn.
