# Upgrading blanket

How blanket's database is versioned, backed up and migrated; how a running
server is restarted onto a new binary; and what to do when something goes
wrong in the middle of it. `blanket upgrade` / `blanket rollback` drive all
of it.

## The short version

```
blanket upgrade --check     # is there a newer release?
blanket upgrade --yes       # install it and restart onto it
blanket rollback --yes      # change your mind
```

`blanket upgrade` verifies the download against the release's
`SHA256SUMS`, keeps the binary it replaces in a [rollback
slot](#rollback-slots-blanket-rollback), takes a database backup before
anything moves, and walks the running server through the [restart state
machine](#the-restart-state-machine). Everything it does is also
[printable](#--print-plan) and doable by hand.

### Upgrading from before v0.3.0

Releases before v0.3.0 publish no `SHA256SUMS`, so `blanket upgrade`
cannot verify them and will not install them. Upgrading *from* one is
unaffected, but the first move to a verified release is by hand:

1. Stop the server.
2. Replace the binary (or `blanket upgrade --bundle ...`, see [offline
   bundles](#offline-bundles)).
3. Start it again.

Your existing database is read as **schema version 1** and stamped as such
on the first open. Nothing else changes: no records are rewritten, no
backup is taken, no migration runs. There are no migrations yet; blanket
ships the machinery with zero migrations in it, so the first real schema
change is not also its first exercise.

## Schema versions

The database carries a schema version in a `meta` bucket, alongside four
other facts about the installation rather than the work in it:

| Key | What it holds |
| --- | ------------- |
| `schemaVersion` | The schema the file is written in. Missing means version 1. |
| `serverInstance` | Which server process last owned this database, and when it started — the same `instanceId` a worker's heartbeat response carries. |
| `lockHolderPid` | The process holding the database's exclusive lock. Written on open, cleared on a clean close, so a record left behind means the previous blanket crashed. |
| `migrationMarker` | Set while a migration is in flight; see [the migration marker](#the-migration-marker). |
| `restartRecord` | What the restart state machine is doing, and what the next process owes the workers. Absent when nothing is in flight. See [the restart state machine](#the-restart-state-machine). |

The `meta` bucket is created on **every** open if it is missing, so an
older database becomes a current one simply by being opened. No
`schemaVersion` key means version 1 by definition: the pre-`meta` layout
*is* version 1.

### The lock-holder sidecar: `blanket.db.lock.json`

A process that is locked *out* cannot read the `lockHolderPid` record,
since even a read-only BoltDB open needs the lock. So the same record is
also written to a sidecar file beside the database,
`blanket.db.lock.json`, which turns

```
could not acquire lock on bolt database "blanket.db" after 5s: is another blanket process already running?
```

into

```
could not acquire lock on bolt database "blanket.db" after 5s: pid 4213 on hostname has it open (since 2026-09-06T21:02:11Z)
```

The sidecar is a hint, not an authority: a recorded pid that is
conclusively dead is reported as a stale lock rather than a live holder,
so the message never sends you to kill an innocent process that inherited
the pid. The wait is `storage.openTimeout` (default `5s`; see [config
keys](#config-keys)).

## Migrations

Migrations are **forward-only** and numbered. There are no `Down()`
functions (an inverse migration is code run approximately never and
therefore tested by nobody); going back means putting a file back, which
is what `--restore` does. Two properties make this safe:

- **A backup is mandatory before any migration.** If the backup cannot be
  taken, the migration does not run.
- **Each migration's data change and its version bump commit in one
  transaction.** A crash mid-migration leaves either both applied or
  neither — never a half-migrated file that claims to be the old version.

### When migrations run

Automatically, when the server opens the database — not on demand, and not
after a prompt. Under a supervisor, a server that refused to start until a
human ran `blanket migrate` would crash-loop, and the crash-looping server
and the human's `migrate` would race for the database lock. Auto-applying
forward, after a backup, avoids that livelock.

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

The last case refuses rather than restoring automatically: an automatic
restore silently discards whatever happened between the backup and the
crash, and under a supervisor one that itself fails rewrites the database
on every boot. The error names the fix:

```
a migration v1→v2 started at 2026-09-06T21:02:11Z never finished; the database is still at version 1.
Restore the pre-migration backup before starting blanket again:

    blanket migrate --restore /var/lib/blanket/backups/blanket-1-2026-09-06T21-02-11.418Z.db --yes
```

## Backups

A backup is a consistent point-in-time copy taken through BoltDB's
`tx.WriteTo` inside a read transaction, so it is valid **while the server
is serving** — no pause, no downtime. A `cp` of a live database gives no
such guarantee.

### Backup location and filenames: `storage.backupDir`

Backups go to `<the directory holding your database>/backups/`, or
wherever `storage.backupDir` points. Filenames are
`blanket-<schemaVersion>-<timestamp>.db`, e.g.
`blanket-1-2026-09-06T21-02-11.418Z.db`: the schema version is the fact
you need in order to choose a backup without opening it, and the
timestamp is RFC3339 with `:` replaced by `-` (Windows filenames cannot
contain a colon), with milliseconds so two backups in the same second do
not collide.

### The free-space precheck

A backup momentarily doubles the database's footprint, so:

| Free space | Behaviour |
| ---------- | --------- |
| less than the database size | **Refused.** Writing would fail part-way and leave a truncated file that looks like a backup. |
| less than 4× the database size | Proceeds, with a warning. Retention keeps 3 backups plus the live database, so below 4× the retention policy cannot be sustained. |
| unknown (a platform with no `statfs`) | Proceeds, and says so. "Don't know" is not "no space", and a backup is mandatory before a migration. |

### Retention: `storage.backupRetention`

The **3 newest** backups are kept and older ones pruned; the count is
`storage.backupRetention`, matching the three [rollback
slots](#rollback-slots-blanket-rollback) whose database half these files
are. There is no size cap: a cap that silently stopped taking backups
would defeat the purpose, so the policy is a count plus a warning. Pruning
happens *after* a new backup is written, never before, so a full disk
cannot delete a good backup and then fail to write its replacement. Files
that do not match the `blanket-*.db` naming are never touched.

## Commands

### `blanket backup`

Writes a consistent copy of the database, whether or not the server is
running. It tries the server first — `POST /ops/backup`, the only way to
back up a live install, since the CLI cannot open the database while the
server holds its lock — and opens the database directly only when no
server answers. A server that answers and *refuses* is reported as a
refusal; the CLI does not go behind its back.

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

`--check` opens the database read-only and writes nothing, not even the
`meta` bucket. It prints the database path, whether it is stamped, and the
current and target schema versions, and exits **0** when nothing is
pending, **1** when something is (pending migrations, a stale marker, a
too-new database), and **2** when it could not tell (locked, missing, or
unreadable), so scripts can branch without parsing prose.

`blanket migrate` with no flags runs the *same* code path the server runs
at open, not a second implementation that could disagree with it.

`--restore` makes three checks before it replaces anything:

1. **The database must not be in use.** It proves that by taking the lock
   itself. Overwriting the file underneath a running server restores
   nothing — BoltDB has the old pages mapped and flushes them back over
   the new contents on the next write.
2. **The backup is verified first**, while the live database is still the
   only thing anyone relies on.
3. **What it replaces is kept**, renamed to
   `<database>.pre-restore-<timestamp>`, so restoring the wrong backup is
   undoable.

`--yes` is required, and there is no interactive prompt: this runs from
scripts, where a prompt is a hang.

## Upgrading with the CLI

`blanket upgrade` is a *driver*. The server owns the backup, the migration
and the restart; the CLI adds what lives outside the process: choosing a
release, verifying the bytes, staging the file without ever leaving a
partial one in place, keeping what it replaced, and journaling each step
so a `kill -9` of the CLI is recoverable.

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
blanket upgrade --json ...                 one JSON object instead of prose
```

`--exec-mode`, `--drain-mode` and `--drain-timeout` apply to the restart
it drives; see [the restart state machine](#the-restart-state-machine).

### The upgrade sequence

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

Every step is written to [the journal](#the-upgrade-journal-journaljson)
before the next one starts.

An upgrade **drains** where a routine restart does not: a new binary
contains new *worker* code, and a worker that rode out the restart is
running the old one. `--drain-mode never` keeps a long task running
instead; see
[`--drain-mode`](#--drain-mode-whether-a-restart-stops-its-workers).

### Verification against `SHA256SUMS` (there is no `--no-verify`)

Every binary is checked against the release's `SHA256SUMS` asset before it
is installed, and there is no flag to turn that off: it would be the flag
every "just make it work" answer told people to pass.

> **Releases before v0.3.0 carry no `SHA256SUMS` and cannot be
> auto-upgraded *to*.** `blanket upgrade v0.2.0` refuses and says so.
> Install such a release by hand, or build a [bundle](#offline-bundles)
> from it. Upgrading *from* an old release is unaffected.

### How the restart is verified: `instanceId` and version

The last step verifies the *restart*, and it takes two facts: the server
answering on the port reports an `instanceId` different from the one the
CLI read before it started, **and** its version banner names the version
that was installed. The instance id alone would accept the process being
replaced, which keeps serving until its listener closes; the version alone
would accept a server that never restarted (the swap landed, the exec did
not).

A *third* process can pass the instance-id half: an abandoned blanket
parked on the database lock, which wins the port the moment the real
server lets go — but it runs the old binary, and the version catches it.
The wait keeps waiting while something on the wrong version answers, up to
its 90-second budget, then reports the version it saw (see
[troubleshooting](#upgrade-and-restart-errors)).

The CLI avoids creating such a process itself: when it has to start the
replacement (`--exec-mode=exit` on an unsupervised box) it waits for the
port to be quiet **and stay quiet** for a few seconds, since a server
re-execing in place stops answering for a moment; and "is there a server
here at all?" is asked with a short retry, so an install between process
images is not read as *stopped*. If the instance id could not be read
before the restart, the CLI warns and verifies on the version alone.

### Exit codes

`blanket rollback` uses the same codes.

| Code | Meaning |
| ---- | ------- |
| `0` | Done: the new binary is installed and a new server answered on it. For `--check`, **an upgrade is available**. |
| `10` | **Nothing to do** — the installed binary is already the target version and the server on the port runs it too (or no server answered, or it is a dev build whose version can't be read), or nothing to roll back to. |
| `11` | **Staged only** — the binary is staged or installed and no restart was attempted (`--stage-only`, `--no-restart`, or no server was running). Also when the installed binary is already the target version but the server on the port still runs another one; see below. |
| `12` | **Verification failed** — a checksum did not match, or the server that came back is not the one that was installed. |
| `13` | **Restart refused** — the binary is in place but the server would not restart: a 409 from the state machine, a restart already in flight, or a `--resume` that found the old server with no restart in flight. |
| `1` | Any other error. |
| `2` | Usage: the flags don't make sense, or a precondition a human must fix is unmet (including a missing `--yes`). |

`--check` exits **0** when there *is* something to do, the opposite of
[`blanket migrate --check`](#blanket-migrate): `migrate --check` is a
health probe (pending is the abnormal case), while `upgrade --check` asks
"would this command do something?".

Before it says "nothing to do", with or without `--check`, the CLI asks the
server on the port for `GET /version`. The CLI is the installed binary, so
after a swap whose restart failed, or after `--no-restart`, the binary is
current while the old process is still serving. In that case it exits
**11** instead of 10:

```
$ blanket upgrade --yes
blanket v0.5.0 is installed at /home/you/.local/bin/blanket, but the server on :8773 is still running v0.4.0.
Run `blanket upgrade --resume --yes` to finish the restart.
```

The second line is `--resume --yes` when [the journal](#the-upgrade-journal-journaljson)
records an attempt that is neither `VERIFIED` nor `ABORTED` and the server
still has a restart in flight; otherwise it says to restart the server
under its supervisor, since `--resume` refuses when nothing is restarting. `--json` reports the
server's version as `serverVersion` (empty when no server answered or its
version can't be read).

### `--print-plan`

Prints the [manual `curl` sequence](#restarting-by-hand) with *this*
install's port, paths, slot directory and target version substituted in,
and stops. It is the same steps in the same order; the command runs them
over HTTP instead of through `curl`.

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

**The installed path is never a partial file.** The download goes to a
temp file *in the same directory* as the installed binary (another
directory could be another filesystem, making the last step a copy rather
than a rename), is verified there, and is then renamed into place, which
is atomic within a directory. On unix that rename is the whole swap; the
kernel keeps the old inode alive for the running process until it
re-execs. **Windows** refuses to replace a file that is executing, so
there the running `blanket.exe` is renamed aside first
(`blanket.exe.old-<ts>`) and swept on the next `blanket upgrade` or
`blanket rollback`, once the process holding it is gone.
`scripts/install.sh` and `scripts/install.ps1` follow the same
download-verify-rename pattern.

### Rollback slots: `blanket rollback`

Each upgrade keeps the binary it replaced, with the name of the database
backup taken just before it, in a **slot** under
`<state dir>/slots/<timestamp>-<version>/`. Three are kept and older ones
pruned (`upgrade.slots`), matching `storage.backupRetention`: the database
half of a slot *is* one of those backup files, so a fourth slot's backup
would already be gone. As with [backups](#retention-storagebackupretention)
there is no size cap and pruning happens *after* the new slot is written;
instead an upgrade **warns** when the filesystem holding the slots has
less room than three more copies of the binary would need.

To roll back an upgrade:

```
blanket rollback --list               what can be rolled back to
blanket rollback --yes                the newest slot
blanket rollback --slot <dir> --yes   a specific one
blanket rollback --restore-db --yes   also restore that slot's database backup
blanket rollback --json ...           one JSON object instead of prose
```

The saved binary is verified against the digest recorded when it was
saved, before it is put back. A rollback also keeps a slot of what *it*
replaced, so it is itself undoable.

**`--restore-db` is opt-in and the binary is not.** Putting the binary
back loses nothing; putting the database back discards every task, worker
record and queue entry created since the backup was taken. It also
requires the **server stopped**, for the same reason [`blanket migrate
--restore`](#blanket-migrate) does.

### The upgrade journal: `journal.json`

The CLI's own state, at `<state dir>/journal.json`: the half of the
restart state that [lives outside the
database](#why-the-state-lives-in-two-places), because what `--resume`,
`--abort` and `blanket rollback` need (the staged binary, the previous
binary, the backup taken) must survive the next boot, which is what erases
the server's record. One JSON object, rewritten in full on every
transition via a temp file plus rename; fields are additive-only and
unknown ones are ignored on read, since reading the journal is exactly
what a downgrade does.

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
`DRAINED`, `EXECED`, `VERIFIED`, `ABORTED`, `FAILED`. These mirror the
server's states where the two coincide, so a journal and `GET
/ops/restart/status` read as the same story; `PLANNED` and `STAGED` are
the driver's alone, since the server never sees a download.

### `--resume` and `--abort`

| Journal state when the CLI died | `--resume --yes` | `--abort` |
| ------------------------------- | ---------------- | --------- |
| `PLANNED` | Refuses. A partial download cannot be resumed — the only safe thing to do with an unverified file is throw it away. Run the upgrade again. | Removes the staging file. |
| `STAGED` | Re-verifies the staged binary against the recorded digest, then continues from `begin`. | Removes it; nothing was installed. |
| `BACKED_UP`, `PAUSED` | Re-verifies the staged binary and continues, skipping the transitions the server has already made. If the staged file is **gone**, checks whether the installed binary is already the one that was staged (below). | Clears the server-side restart (un-pausing worker spawn); nothing was installed. |
| `SWAPPED`, `DRAINED`, `EXECED` | The new binary is already installed; finishes the restart. If a server answers, its own `resolvedExecMode` decides whether the CLI has to start the replacement or the server is bringing itself back. If the server answers with **no restart in flight** and is still the old process or version (the watchdog aborted the restart, or `--no-restart` never began one), refuses with exit `13`: restart the server under its supervisor to start the new version. A resume never begins a new restart. | Clears the server-side restart and **leaves the new binary in place**, naming `blanket rollback` as the way back. |
| `FAILED` | Goes by the furthest state the journal reached (below). Failed **before** the swap: as `STAGED`, re-verifying the staged binary, or refusing if there is none. Failed **after** the swap: as `SWAPPED`, `DRAINED` or `EXECED`, finishing the restart. | Goes by the furthest state reached: before the swap, nothing was installed; after it, the binary is left in place and `blanket rollback` named, as for `SWAPPED`. |

`FAILED` is where an attempt ended, not how far it got, so neither command
decides from it alone. The journal's `steps` record every transition, and
the highest-ranked one says whether the binary was swapped. An attempt that
failed in the drain has consumed its staged file by installing it, and
`--resume --yes` finishes its restart the same way it would after a
`kill -9` at `SWAPPED`.

Whenever the restart `--abort` cleared had already drained workers, it
warns that any worker not back needs `PUT /worker/:id/restart`. A current
server brings drained workers back on abort; v0.3.0 and earlier cleared
their respawn intent instead and left them stopped.

Forward-only means "do the transitions that have not happened yet", which
is both the first-run path and the resume path: the CLI reads the server's
current state and skips ahead. A restart in flight that is *not* this
attempt (matched by id) is refused, because two drivers racing over one
state machine is how an install ends up paused with nobody to un-pause it.

`--abort` always tries `POST /ops/restart/abort` first, journal or no
journal: a paused server is the failure mode that outlives everything
else, and the one an operator typing `--abort` most needs undone.

#### Resuming when the swap happened but the journal says `PAUSED`

The CLI saves the rollback slot, renames the staged binary into place, and
only then records `SWAPPED`. Killed between the rename and the journal
write, it leaves a journal that still says `PAUSED` and names a
`stagedPath` that is gone, because it *became* the installed binary. A
resume that trusted the journal would copy the **new** binary into a
rollback slot named for the **old** version, and a later `blanket
rollback` would install the version it was asked to undo.

So the resume asks the disk: the journal's `sha256` is the digest of the
staged file, and an installed binary hashing to it is proof the rename
completed. The slot written just before the rename is found again by
`upgradeId`, which is why slots carry one. Anything else is refused — a
resume that cannot tell which world it is in must not pick one.

### Offline bundles

A **bundle** is one file containing everything an install needs and
nothing it has to fetch: the binaries for all three platforms, the
`SHA256SUMS` that covers them, the example task types, the
`blanket-task-type` skill, the install scripts, and a `manifest.json`
naming the version. Releases attach one as
`blanket-bundle-<version>.tar.gz`; `make bundle` builds one locally.

```
blanket upgrade --bundle blanket-bundle-v0.5.0.tar.gz --yes
```

`--bundle` accepts the tarball or an already-extracted directory.
**Verification is identical to the online path**: the bundle carries the
same `SHA256SUMS` and the binary is checked against it before it is
installed; the bundle changes where the bytes come from, not whether they
are checked. [offline_install.md](offline_install.md) covers installing
from one.

### The update notice: `blanket ps`

`blanket ps` prints one line when a newer release is known:

```
A newer blanket is available: v0.5.0 (you have v0.4.0). Run `blanket upgrade` to install it.
```

**This never slows the CLI down.** The message comes from a cache
(`<state dir>/notice.json`) that an *earlier* run wrote; a cold cache
prints nothing rather than fetching. The refresh runs at most once a day,
in a background goroutine with a hard 250ms deadline (so a firewall that
blackholes packets costs no more than an unreachable GitHub), and writes
the cache for the next invocation. The check-stamp is written even when
the fetch fails, or an offline machine would retry on every invocation.

It stays quiet when stdout is not a terminal (so `blanket ps | wc -l` is
unaffected), under `--json`, and entirely when
`upgrade.checkForUpdates: false`.

### Windows: the CLI restarts the server

Windows never self-restarts. A detached replacement escapes a service's
job object, so `sc stop blanket` cannot reach it and it holds the database
lock invisibly — the worst failure mode in the system. So `--exec-mode`
degrades to `exit` there, and after the old server stops the upgrade CLI
**starts the replacement itself**. Under the service control manager it
prints the command instead:

```powershell
sc start blanket
```

## The restart state machine

Replacing a running blanket is a sequence driven from **outside the
process** — `blanket upgrade`, or `curl` — and a machine can lose power
between any two steps. So the sequence is written down one state at a
time, every step is an HTTP call ([api.md](api.md#restart-endpoints) has
the request and response shapes), and every state has a documented
recovery.

### Why the state lives in two places

The driver cannot open the database: BoltDB's lock belongs to the server
for as long as it is up, and the interesting part of a restart is the
window where the server is going away and coming back. So the **restart
record** in the database's `meta` bucket is what the *next server process*
reads on boot to find out what it is the continuation of, and the
**journal file** owned by the driver is what a *human* reads when the
server is down and did not come back. Nothing in the server reads the
journal, so its format belongs to the CLI; see [the upgrade
journal](#the-upgrade-journal-journaljson).

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

### Every transition is one transaction

Two facts that must agree are never written in two transactions. The case
that matters is the drain: "this restart is at `DRAINING`" and "these
workers are stopped and are to be brought back" commit together, so a
crash cannot leave workers stopped that nothing will restart, or a promise
to restart workers that are still claiming tasks.

### What happens if the machine dies mid-restart

Whatever state it was in, **the next server clears the record**: a boot is
the strongest evidence that the process which wrote it is gone (it held
the database lock, and this one has it), and staying paused would leave
an install unable to start a worker, permanently. The obligation to
*specific workers* survives separately, as respawn intent on the worker
records, which the boot does not touch. The record is the plan; the
intents are the debt.

| Died at | What the next server does | What you do |
| ------- | ------------------------- | ----------- |
| `STAGED` | Clears the record. Nothing had changed. | Start again. |
| `BACKED_UP` | Clears the record. The backup is still on disk, in `backups/`. | Start again; you can reuse the backup. |
| `PAUSED` | Clears the record, so spawn works again. | Start again. |
| `SWAPPED` | Clears the record. **The binary on disk may be the new one and the running server the old one** — now simply "a server running an old image", which the next restart fixes. | Start again; it will be a very short one. |
| `DRAINING` | Clears the record, then respawns every worker carrying an intent. | Nothing, unless a worker is missing — check its `stoppedReason`. |
| `EXECING` | Same as `DRAINING`. The exec never happened; the debt to the workers did. | Nothing. |

`scripts/test/restart_machine.sh` asserts every row of that table against
a real process, by killing the server at each state in turn
(`BLANKET_TEST_CRASH_AT`) and checking the replacement's behaviour.

### Worker respawn, and the three storm guards

A drain records a respawn intent on each worker it stops. The intent is
cleared **after** a successful spawn, which makes respawn at-least-once:
losing a worker on an upgrade is a silent, lasting failure, while spawning
one twice is loud and self-correcting. Three guards keep "at least once"
from becoming "forever":

1. **Pid liveness.** Before spawning, the server checks whether the worker
   is already running (pid paired with the pid's start time, so a recycled
   pid cannot masquerade). If it is — the normal outcome when the previous
   process died after forking but before clearing the intent — the intent
   is settled by observation rather than a second fork.
2. **A generation cap.** Three attempts, counted *before* the fork so the
   cap converges even when the spawn is what kills the server. Past that
   the intent is dropped and the worker left stopped with `stoppedReason:
   "restart: gave up respawning after N attempts"`.
3. **A minimum interval.** 30s between two respawns of the same worker,
   so a server crash-looping under a supervisor does not fork the whole
   fleet on every boot.

### The deadline watchdog: `restart.deadline`

The driver is a separate process and can be `kill -9`ed. The record
therefore carries a deadline that **every transition refreshes** (a
watchdog, not a budget), and the server aborts the restart when it lapses,
lifting the pause and bringing back anything the drain stopped. That is
the same code `POST /ops/restart/abort` runs, so the recovery a human
triggers is the one every timeout has already exercised.

### Restarting by hand

The full sequence, in the order `blanket upgrade` runs it. Every call
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

# 2. Back up while the server is still serving; advances the record to
#    BACKED_UP and records the path.
curl -sS "${OPS[@]}" -X POST $BASE/ops/backup

# 3. Stop worker spawn, so none is forked from a binary about to be
#    replaced. From here, POST /worker/ answers 409.
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/pause

# 4. Swap the binary (you, or your package manager), then say so.
install -m 0755 ./blanket-new "$(command -v blanket)"
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/swapped

# 5. Drain — only if the new binary changes worker behaviour; a routine
#    restart skips this. Waits up to restart.drainTimeout; answers
#    `drained: false` with a list if something is still running.
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/drain

# 6. Go. 202, then the server tears down and either re-execs in place or
#    exits 75 for its supervisor — see --exec-mode below.
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/exec

# 7. Verify. A different instanceId means a different process came back.
until curl -fsS $BASE/version >/dev/null 2>&1; do sleep 0.5; done
curl -sS "${OPS[@]}" $BASE/ops/restart/status   # -> IDLE
curl -sS $BASE/config/ | grep instanceId

# Changed your mind at any point before step 6:
curl -sS "${OPS[@]}" -X POST $BASE/ops/restart/abort
```

### `--exec-mode`: how the server is replaced

| Value | What `exec` does |
| ----- | ---------------- |
| `auto` (default) | Under a supervisor, **exit**; unsupervised, **re-exec in place**. |
| `exec` | Always re-exec in place: `syscall.Exec` over the binary at `os.Executable()`, same argv, same environment. The pid is preserved, so a server started under `nohup` or in tmux keeps its terminal and its logs. |
| `exit` | Always drain and exit **75**, leaving the restart to whatever started this process. |

`auto` exits under a supervisor because a supervised process that re-execs
itself is invisible to the thing managing it, and escapes whatever it
would have done about a bad new binary. "Supervised" is a **heuristic**:
systemd's `INVOCATION_ID` / `JOURNAL_STREAM` / `NOTIFY_SOCKET` /
`LISTEN_PID`, or launchd's `XPC_SERVICE_NAME`. Set `BLANKET_SUPERVISED=1`
(or `=0`) for a deployment it cannot recognise, or say what you mean with
`--exec-mode`. `GET /ops/restart/status` reports both the configured mode
and the resolved one.

The exit code is **75** (`EX_TEMPFAIL`), not 0: the systemd unit blanket's
installer writes says `Restart=on-failure`, so a clean exit is the one
thing a supervised server must not do when it wants to come back.

On **Windows** `exec` degrades to `exit`, and you or the upgrade CLI start
the server again; see [Windows](#windows-the-cli-restarts-the-server).

### `--drain-mode`: whether a restart stops its workers

| Value | Behaviour |
| ----- | --------- |
| `auto` (default) | Drain only when the caller asks, by calling `POST /ops/restart/drain`. |
| `always` | `exec` drains first, whether or not the caller asked. |
| `never` | `POST /ops/restart/drain` is refused with 409. |

Routine restarts do not drain: a worker rides out a server outage by
design (it retries with backoff and keeps its task running), so stopping
the fleet for a config change would cost real work for nothing. Drain when
the *worker* code changes — an upgrade — which `blanket upgrade` does and
`always` makes unconditional.

## Config keys

| Key | Default | What it does |
| --- | ------- | ------------ |
| `storage.openTimeout` | `5s` | How long to wait for the database's exclusive lock before giving up. Under `Restart=always` the supervisor starts the replacement immediately, so this must outlast the old process's drain-and-teardown or a routine restart becomes a crash loop. |
| `storage.backupDir` | `""` | Where backups go. Empty means `<database dir>/backups`. |
| `storage.backupRetention` | `3` | How many backups to keep. |
| `restart.execMode` | `auto` | How a requested restart replaces the process: `auto`, `exec`, `exit`. Also `--exec-mode`. |
| `restart.drainMode` | `auto` | Whether a restart stops its workers: `auto`, `always`, `never`. Also `--drain-mode`. |
| `restart.drainTimeout` | `60s` | How long a drain waits for stopped workers to exit before reporting the stragglers. A bound on the *wait*, never on the task — a worker finishes what it is running first. Also `--drain-timeout`. |
| `restart.deadline` | `5m` | How long the watchdog gives the restart's driver between transitions before aborting. Generous: the steps it spans include a human swapping a binary by hand. The drain is one transition, so **if you raise `restart.drainTimeout` past this, raise this too** (to the drain timeout plus a few minutes): otherwise the watchdog aborts the restart mid-drain and `blanket upgrade` exits 13 once the drain returns. |
| `upgrade.checkForUpdates` | `true` | Whether the CLI prints [the update notice](#the-update-notice-blanket-ps). |
| `upgrade.stateDir` | `""` | Where the journal, the rollback slots and the notice cache live. Empty means `<database dir>/upgrade`, beside the backups the slots pair with. |
| `upgrade.slots` | `3` | How many [rollback slots](#rollback-slots-blanket-rollback) to keep. |
| `upgrade.repo` | `turtlemonvh/blanket` | Which repository releases come from. |
| `upgrade.releasesBaseURL` | `https://api.github.com` | The Releases API root. Overridable (and `--releases-base-url`, hidden) so `scripts/test/upgrade.sh` can serve a fake releases API off localhost. |

## The ops endpoints

`POST /ops/backup` and the seven `/ops/restart/*` routes are privileged
and local-only: every call must come from **loopback** and carry an
**`X-Blanket-Restart`** header, and `/ops/` is carved out of blanket's
wildcard CORS policy, so a web page you happen to visit cannot drive them.
`curl`, scripts and the CLI are unaffected. [api.md](api.md#ops-endpoints)
has the guard's exact rules and every request and response shape.

## Troubleshooting

### Database lock and schema errors

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

### Upgrade and restart errors

**"a server restart is in flight; worker spawn is paused"** (409 on
`POST /worker/`) — somebody called `POST /ops/restart/pause` and has not
finished. `GET /ops/restart/status` says who and when; `POST
/ops/restart/abort` ends it. It also ends itself after `restart.deadline`,
and never survives a restart of the server.

**"this release publishes no SHA256SUMS"** — you asked to upgrade to a
release before v0.3.0, which [cannot be
verified](#verification-against-sha256sums-there-is-no---no-verify).
Install it by hand, or use `--bundle`.

**"checksum mismatch"** — the bytes that arrived are not the bytes the
release says they are. Nothing was installed and the staged file was
removed. Retry; if it repeats, download the asset and the `SHA256SUMS` by
hand and compare them yourself.

**"a restart is already in flight"** — another driver (or a previous run
that is not this attempt) has the state machine. `blanket upgrade --abort`
or `POST /ops/restart/abort` clears it; it also clears itself after
`restart.deadline`.

**"a server came back but reports &lt;the old version&gt;"** — something is
serving the port that is neither the process that was replaced nor the one
that was installed, usually a second blanket started against this install
that has been sitting on the database lock. Check for another process
running the same binary (`ps` for the installed path, `<state dir>/server.log`
if the CLI started one), stop it, and run the command again. `blanket
rollback --yes` puts the previous binary back if the install itself is
what is wrong.

**The upgrade exited 12 and the server did not come back** — the new binary
*is* installed. Start it by hand to see what it says, or `blanket rollback
--yes`. `<state dir>/journal.json` records how far the attempt got.

### After an upgrade

**A `blanket.exe.old-*` file next to the binary (Windows)** — the running
`.exe` that a swap moved aside. It is removed by the next `blanket upgrade`
or `blanket rollback`, once the process holding it has exited.

**A worker did not come back after an upgrade** — check its
`stoppedReason`. `restart: gave up respawning after 3 attempts` means the
server tried and the worker would not start; its own logfile
(`GET /worker/:id/logs`) says why. An empty reason with `stopped: true`
and no `respawnIntent` means somebody stopped it explicitly during the
restart, which is a decision that outranks the respawn.
