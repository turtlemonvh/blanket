# Authentication — design brief

*Review only the sections above the appendix. Comment "ok" to approve all
defaults, or comment on the specific decision row you want changed. Anything
not commented on is treated as approved.*

**Issue:** #101  **Autonomy after approval:** pr-only
**Risk:** high  **Written:** 2026-10-24, by Claude from the #44/#101
discussion with @turtlemonvh; sources in [Appendix B](#appendix-b-evidence-and-alternatives).

### Goal

Every surface (REST, MCP, UI, workers, `/ops`) can sit behind scoped tokens
and an admin login, switched on in minutes. Not in scope: OAuth,
internet-facing use, multi-machine workers (#235).

Settled on #101: [A.1](#a1-already-agreed-on-101). The rows
below are what is still open.

### Decisions

| # | Decision | Default I will take | Alternative considered | Why default | Reversible? |
|---|---|---|---|---|---|
| 1 | Permission model | Seven scopes plus four presets (viewer, agent, operator, admin) | Fixed roles | Presets stay simple; scopes allow a submit-only agent token | yes |
| 2 | Per-resource grants | Tokens can be limited to listed task types, or granted read on listed task ids | Per-task ACLs | Covers "only run `transcribe`" and "share one task" | yes, additive |
| 3 | Enforcement | One deny-by-default route→scope table, tested for full coverage. Loopback is no longer trusted; `/ops/*` needs `admin` | Per-handler checks | A forgotten route fails closed; `tailscale serve` makes every peer loopback | yes |
| 4 | Bootstrap and on/off | Auth is on iff an admin or non-worker token exists. Init needs the setup code (logged, and in a 0600 file by the DB) and writes an admin credential file for the local CLI. Off = `blanket auth disable`, locally | Config flag; CLI opens the DB | No peer can claim admin; Bolt is locked while running | yes |
| 5 | Browser sessions | Bolt-backed sessions, HttpOnly SameSite=Lax cookie; CSRF token on *every* cookie-authenticated non-GET request | Signed cookie; CSRF on `/ui/*` only | Another port on the same host is same-site, so Lax alone lets a form POST `/task/` | yes |
| 6 | Worker tokens | Minted per worker, bound to its id, set only on the child's `cmd.Env`, stripped before any task runs | One shared token | Tasks inherit the worker's env (`tasks/tasks.go:196`) | yes |
| 7 | Token lifetime | No expiry unless `--expires`; last-used shown | 90-day default | Silent expiry breaks agent configs | yes |
| 8 | Login rules | A passkey is a full sign-in; failed passwords add backoff capped at 30s, never a lockout; TOTP secrets stored in the DB | Passkey as one factor; hard lockout; key-encrypted secrets | Passkeys are two-factor already; lockouts let anyone lock out the admin; TOTP must be reversible, unlike hashed passwords and tokens | yes |

### What changes for you

- `blanket auth init`: admin, token, credential file, worker relaunch.
- `blanket token create|list|revoke`, `blanket user add|passwd|reset-2fa`.
- MCP configs gain an `Authorization: Bearer blk_…` header.

### Risks I am accepting

- **A wrong route-table row exposes code execution** (mitigated by row 3). The one to stop on.
- Reading the server's files makes you admin, and a DB copy can clone TOTP (rows 4, 8).
- Bearer tokens are cleartext over plain HTTP off-tailnet.
- Old workers get 401s until relaunched; `auth init` relaunches them.

### Plan shape

Each phase ships alone.

1. Tokens, scopes, middleware, `auth init`, token CLI (off by default).
2. Worker tokens.
3. UI login: users, sessions, CSRF, setup code, CLI reset, banner.
4. TOTP and recovery codes.
5. Passkeys via `server.publicUrl`.

### Resolved in review (2026-10-25)

- MCP `tools/list` shows only tools the caller's scopes allow.
- `mcp.mode` stays: it sets which tools exist (`readonly`/`create`/`all`); scopes only narrow it.
- Multiple users in the schema now; CLI first, UI management in phase 3.
- A token can always read and cancel tasks it created, in addition to its other grants. Phase 1.

## Appendix A: full design

### A.1 Already agreed on #101

| Area | Agreed |
|---|---|
| Tokens | `blk_` + 32 random bytes (base32). Only the SHA-256 hash is stored. No signing key, no JWT. |
| OAuth | Deferred. The token table is designed so OAuth can later be another way to issue tokens. |
| Passwords | argon2id, OWASP default parameters (m=19 MiB, t=2, p=1); the salt is stored inside the encoded hash. |
| 2FA | Password first; TOTP is an optional second step, enrolled by QR code, with recovery codes. |
| Passkeys | Supported behind `tailscale serve` / Tailscale Services. The relying-party ID and origin come from `server.publicUrl`. |
| Admin bootstrap | From the CLI, or from the UI with a one-time setup code printed to the server log (as Jenkins and Jupyter do). The first visitor never becomes admin. |
| Recovery | Password reset from the CLI, which also clears TOTP. |
| Workers | Worker-only scope; the token is passed by env var. Multi-machine workers are #235. |
| Default | Auth is off until set up: a startup warning, a dismissable UI banner, and a permanent warning on the admin page. `/version` stays open (#101 also said `/health`, but no such route exists). |
| Done means | Bootstrapping is easy for new installs and for existing ones being locked down. |

### A.2 Scopes and presets

| Scope | Grants |
|---|---|
| `read` | `GET`/`HEAD` on tasks, task logs, `/results/*`, task types, workers, worker logs, `/schedule/describe`, the `/ui/*` pages and partials, and SSE streams; MCP read tools; `/ops/status/` |
| `submit` | `POST /task/` (including `?wait`), the UI new-task form, MCP `blanket_submit_task` and `blanket_run_task` |
| `tasks:manage` | Cancel, delete, pause, resume and reschedule tasks; MCP `blanket_cancel_task` |
| `types:write` | MCP `blanket_write_task_type` and any future REST write endpoint |
| `workers:manage` | Launch, stop, restart and delete workers; MCP `blanket_launch_worker` and `blanket_stop_worker` |
| `worker` | `POST /task/claim/:workerid`, `PUT /task/:id/{run,progress,finish}`, `PUT /worker/:id{,/heartbeat}`, all bound to the token's worker id |
| `admin` | Everything, plus users, tokens, `/config/`, and `/ops/*` other than `/ops/status/` |

| Preset | Scopes |
|---|---|
| viewer | `read` |
| agent | `read`, `submit` (usually restricted to listed task types) |
| operator | `read`, `submit`, `tasks:manage`, `types:write`, `workers:manage` |
| admin | `admin` |

`types:write` combined with `workers:manage` and `submit` amounts to code
execution as the blanket user, so operator is effectively admin-equivalent on
the host. The UI says so when issuing an operator token.

**Restrictions (row 2).** A token may carry `types: [...]`. `submit` is then
refused for other types, and `read` lists and shows only tasks of those types.
It may also carry `tasks: [...]`, read access to just those task ids, which is
the "share one task" case. Restrictions only narrow what a token's scopes
allow, with one addition: a token can always read tasks it created
(`createdBy`), even ones its `tasks:` list doesn't name, and can cancel them
without `tasks:manage`, so an agent can stop its own mistake.
`/results/<taskid>/…` honors restrictions too: the first path segment is the
task id (`tasks/task_types.go:378`), so the middleware loads that task and
applies the same type and id checks before the static handler runs. HEAD
follows GET everywhere; the `/` redirect is open.

### A.3 Enforcement

- Gin middleware resolves the caller in this order: `Authorization: Bearer`,
  then the session cookie, then anonymous. It looks up the route's required
  scope in one table keyed by method and the route's path pattern.
- A route that is not in the table requires `admin`.
- `TestEveryRouteHasAScope` walks `router.Routes()` and fails on any route
  missing from the table.
- Always open: `/version`, `/ui/static/*`, the manifest, the login
  and setup pages.
- `/mcp`: the scope check runs per tool. The SDK's `getServer(*http.Request)`
  callback returns a server built for the caller's scope set and `mcp.mode`,
  cached by that set, so `tools/list` already reflects what the caller may do.
- Auth is off exactly when no admin user and no non-worker token exist
  (row 4). Then the middleware allows everything and sets a header the UI
  uses to show the banner.
- The session cookie is accepted on any route, but every non-GET request
  authenticated by cookie must carry a valid `X-CSRF-Token` (row 5).
- `opsGuard` keeps its header and CORS layers. When auth is on it also
  requires `admin`, and loopback alone no longer passes.

### A.4 Data model

These are new Bolt buckets. Creating them is migration v<N+1> in
`lib/bolt/migrations.go`: additive, with the automatic pre-migration backup.

- `users`: id, name, role preset, argon2id hash, TOTP secret (stored as
  is, row 8: the server must compute codes from it, so it can't be hashed
  the way passwords and tokens are; DB backups therefore hold it too), recovery code hashes, failed-login count and backoff-until,
  created, disabled.
- `tokens`: SHA-256(token) → id, name, owner (a user id, or a worker id for
  worker tokens), scopes, restrictions, created, expires, lastUsed.
  `lastUsed` writes are batched (at most once a minute per token) so Bolt's
  single writer isn't hit on every request.
- `sessions`: SHA-256(session id) → user id, created, lastSeen, csrf token,
  `mfaPending` flag.
- `webauthn`: credential id → user id, public key, sign count (phase 5).
- `meta`: `authEnabledTs`, and the setup-code hash while setup is pending.

### A.5 Flows

**Setup code.** While auth is off, the server generates a one-time code at
startup, prints `setup code: XXXX-XXXX` to its log, writes it to
`<dataDir>/setup-code` (0600), and keeps only its hash in `meta`. The code
is the proof of being the operator: either seeing the log or reading that
file. A tailnet peer has neither.

**`blanket auth init`** (works while the server is running):
1. The CLI reads `<dataDir>/setup-code` and calls `POST /auth/init` with
   it and the admin username and password (prompted for). Without a valid
   code the route answers 403, even while auth is off.
2. The server creates the admin and an admin token, writes the token to
   `<dataDir>/credentials` (0600), and deletes the setup-code file. Auth is
   now on, because an admin exists.
3. The CLI prints the token once and offers to relaunch the running workers
   with worker tokens (the phase 2 machinery).

On a fresh install with the server stopped, the CLI opens the DB directly and
does the same.

**UI path.** While auth is off, the UI's "Set up authentication" page asks
for the setup code, then the admin username and password, and calls
`/auth/init`.

**`blanket auth disable`** reads `<dataDir>/credentials`, then deletes all
users, sessions and non-worker tokens through the API. That makes auth off
by the row 4 test.

**The CLI on the server's host** reads `<dataDir>/credentials`. On any
other host it reads `BLANKET_TOKEN` or `--token` (related: #21).
`blanket user passwd|reset-2fa` and `token create` go through the API using
the local admin credential.

**Login.**
1. Username and password → argon2id check.
2. If TOTP is on, the session is created with `mfaPending`, and only the TOTP
   page works until a valid code clears the flag.
3. Brute-force protection is per *account*, not per IP, because behind
   `tailscale serve` every client is 127.0.0.1. After 5 failures, each
   further attempt waits for a backoff that doubles up to 30 seconds and
   never becomes a lockout (row 8). Passkeys and CLI reset are unaffected.

Sessions last 14 days idle and 30 days at most. Logout deletes the session row.

**TOTP enrollment.**
1. A random 20-byte secret, shown as an `otpauth://totp/blanket:<user>?secret=…&issuer=blanket`
   QR code (rendered server-side) alongside the plain-text secret.
2. The user confirms one code before TOTP is switched on.
3. Ten single-use recovery codes are shown once and stored hashed.

Libraries: `github.com/pquerna/otp` and a small QR encoder. Measure the
binary size against #179.

**Passkeys.**
- Offered only when `server.publicUrl` is set to an HTTPS URL and the
  request's `Host` matches it.
- The relying-party ID is that host, e.g. `blanket.<tailnet>.ts.net`.
- A passkey is a full sign-in on its own: no password or TOTP is needed with
  it (row 8).
- Library: `github.com/go-webauthn/webauthn`.
- `docs/examples/tailscale.md` gains a section on it.

**Worker launch.**
1. The server mints a `worker` token bound to the new worker's id. Workers
   are spawned by `buildDaemonCmd` (`worker/worker.go:186`), whose `cmd.Env`
   is currently unset, so the child inherits the server's environment. The
   change sets `cmd.Env` explicitly to `os.Environ()`, minus any inherited
   `BLANKET_TOKEN`, plus `BLANKET_TOKEN=<worker token>`. It never calls
   `os.Setenv` in the server.
2. The worker reads it, calls `os.Unsetenv("BLANKET_TOKEN")`, and only then
   starts its claim loop. `tasks.go`'s `os.Environ()` therefore never sees it.
3. The token is revoked when the worker is deleted.
4. Workers started by hand (`blanket worker`) use the local credentials file
   to request their own worker token.

### A.6 Browser surface

- Sessions use SameSite=Lax cookies, which browsers don't send on cross-site
  subrequests. But a page on another port of the same host *is* same-site, so
  every non-GET request authenticated by cookie, on any route, must carry
  `X-CSRF-Token`. htmx sends it via `hx-headers` on `<body>`.
- The wildcard CORS handler never sets `Access-Control-Allow-Credentials`, so
  a cross-origin page can't use the cookie even where a browser would attach
  it.
- Bearer tokens are not affected by CSRF.

### A.7 Configuration

```
auth.sessionIdleDays   14
auth.sessionMaxDays    30
server.publicUrl       ""     # https URL; enables passkeys (phase 5), reused by #235
```

Whether auth is on is not a config key, so a config edit can't silently
switch it off. See row 4.

## Appendix B: evidence and alternatives

Sources: the #101 and #44 threads; `server/server.go` (routes),
`server/serve_ops.go` (opsGuard), `tasks/tasks.go`, `worker/worker.go`;
`docs/examples/tailscale.md`; OWASP Password Storage Cheat Sheet (argon2id
parameters); the MCP 2026-07-28 authorization spec; RFC 6238 (TOTP);
W3C WebAuthn Level 3 (secure context, RP ID).

- **Signed tokens (JWT).** Rejected on #101: every check happens on the
  server, which already has the DB, so signatures buy nothing and make revoking
  a single token harder.
- **Full MCP OAuth** (RFC 9728 metadata and RFC 8707 audience binding).
  Deferred: only claude.ai remote connectors require it, and they need a
  public endpoint (#191).
- **Trusting Tailscale identity headers** (`Tailscale-User-Login` from
  `tailscale serve`). Attractive as a future zero-config login, but those
  headers can be forged by anything else that reaches the port directly, and
  they only exist on the `serve` path. Recorded as a possible later phase,
  enabled only together with a loopback-only bind.
- **The current `opsGuard` gap.** Under the setup recommended in
  `docs/examples/tailscale.md`, `tailscale serve` connects from 127.0.0.1, so
  every tailnet peer already passes the loopback check today. Row 5 closes this
  once auth is on. Until then it is no worse than the unauthenticated
  `POST /task/`.

## Appendix C: test plan

- Route table: `TestEveryRouteHasAScope`, plus a table test asserting the
  expected status for (preset × route) on representative routes, including
  401/403 bodies.
- Restrictions: a type-restricted agent token gets 403 submitting other types
  and doesn't see their tasks; a task-id grant reads only that task; a
  task-id-granted token that also has `submit` can read the tasks it creates
  as well as the granted one.
- MCP: `tools/list` per preset matches A.2; a read token calling
  `blanket_submit_task` is refused.
- Worker: the token is absent from the task's environment (a task that runs
  `env` doesn't show it); a worker can't claim as, or finish tasks of, another
  worker id.
- Sessions: CSRF missing → 403; logout → the session is unusable; the
  `mfaPending` session only reaches the TOTP page.
- Bootstrap: `auth init` with the server running and with it stopped; the
  setup code is single-use; workers relaunched by init keep claiming.
- Smoke (`scripts/test/smoke.sh`): run the existing flow with auth on, using
  a token.
- Playwright: login, TOTP enroll and login, logout, the banner with auth off.
