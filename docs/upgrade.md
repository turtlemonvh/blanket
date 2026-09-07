# Upgrading blanket

How blanket's database is versioned, backed up, and migrated, and what to
do when something goes wrong in the middle of it.

This page covers the storage half of [issue
#23](https://github.com/turtlemonvh/blanket/issues/23). The restart state
machine (phase 5) and `blanket upgrade` / `blanket rollback` (phase 6) are
not built yet; this page grows to cover them when they are.

## The short version

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
| `restartRecord` | Reserved for the restart state machine (phase 5). Empty today. |

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
`storage.backupRetention`. Three matches the three rollback slots phase 6
will keep — the database half of a slot is exactly one of these files.
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

## Config keys

| Key | Default | What it does |
| --- | ------- | ------------ |
| `storage.openTimeout` | `5s` | How long to wait for the database's exclusive lock before giving up. It was 1s before, which is shorter than a normal shutdown — under `Restart=always` the supervisor starts the replacement immediately, and a budget shorter than the old process's drain-and-teardown turns a routine restart into a crash loop. |
| `storage.backupDir` | `""` | Where backups go. Empty means `<database dir>/backups`. |
| `storage.backupRetention` | `3` | How many backups to keep. |

## The ops endpoints

`POST /ops/backup` is the first of a small group of privileged, local-only
endpoints (`/ops/restart*` joins it in phase 5). All of them are:

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
