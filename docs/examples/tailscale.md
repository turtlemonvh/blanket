# Exposing blanket with Tailscale

Blanket has no authentication. `POST /task/` will run whatever a caller
asks it to run, and the MCP interface can author a task type and launch a
worker for it — arbitrary code execution as the blanket user, by design
(see [MCP interface](../mcp.md#security--read-this-first) and
[issue #101](https://github.com/turtlemonvh/blanket/issues/101)).

That makes the network the access control layer, which is what Tailscale
is good at. This page covers two ways to put blanket on a tailnet, when
to pick each, and the one thing not to do.

Blanket also has no `bindAddress` option yet — it listens on every
interface ([#44](https://github.com/turtlemonvh/blanket/issues/44)). Both
recipes below reach it over loopback, so Tailscale is doing the exposing,
but on a machine that is *also* on an untrusted LAN, remember that port
8773 is open there too.

## Before you start

In the Tailscale admin console, enable **MagicDNS** and **HTTPS
certificates** for your tailnet. Both recipes need them; certificates are
what makes `https://` work without a warning.

Then confirm blanket is up locally:

```bash
curl -s localhost:8773/ >/dev/null && echo ok
```

## Option 1: `tailscale serve` — one command, tied to the device

The simplest thing that works. Run it on the machine blanket is on:

```bash
tailscale serve --bg --https=443 localhost:8773
```

Blanket is now at `https://<device>.<tailnet>.ts.net/` for anyone on the
tailnet, with a real certificate and no port number.

```bash
tailscale serve status          # what this device is serving
tailscale serve --https=443 localhost:8773 off   # stop
```

`--bg` is what makes it survive a reboot; without it the command holds
the foreground and the mapping dies with it.

**Use this when** blanket lives on one machine and you are happy
reaching it by that machine's name. No tagging, no policy file edit, no
admin approval — it works on a personal, user-authenticated device,
which is the case Option 2 explicitly does not cover.

## Option 2: Tailscale Services — a name that isn't a device name

[Tailscale Services](https://tailscale.com/docs/features/tailscale-services)
give the *service* its own name and IP, independent of which machine
happens to host it. Blanket becomes `https://blanket.<tailnet>.ts.net/`
rather than `https://that-old-nuc.<tailnet>.ts.net/`, and moving it to a
new box is a command on the new box rather than a new URL for everyone.

Two prerequisites that are easy to trip over:

- **Tailscale v1.86.0 or later** on the host.
- **The host must use a tag-based identity.** A device you signed in as
  yourself cannot host a service; it has to be tagged (`tag:server` or
  similar). This is the real cost of this option — tagging changes who
  owns the device in your tailnet, and it is not something to do to your
  laptop casually.

Advertise it from the host:

```bash
tailscale serve --service=svc:blanket --https=443 127.0.0.1:8773
```

That one command both configures and advertises the endpoint. Approve
the service once in the admin console's **Services** page, or
pre-approve it in the policy file:

```json
"autoApprovers": {
  "services": {
    "svc:blanket": ["tag:server"]
  }
}
```

Then grant access. Nothing reaches the service until a grant says it
can, and since blanket itself checks nothing, **this grant is your
entire authorization model** — write it as narrowly as you can live
with:

```json
{
  "src": ["autogroup:member"],
  "dst": ["svc:blanket"],
  "ip": ["443"]
}
```

`autogroup:member` is every user in the tailnet. If blanket should be
reachable by you and your CI runner and nothing else, name them instead.

Taking it down, in the right order:

```bash
tailscale serve drain svc:blanket    # stop taking new connections, let existing ones finish
tailscale serve clear svc:blanket    # then remove the endpoints
```

Draining first matters: clearing an advertised endpoint outright kills
every in-flight connection, which for blanket means a `curl` waiting on
a long task.

**Use this when** you want a stable name, or you want to move blanket
between machines without re-teaching anyone the URL.

## One blanket per machine

If you run blanket on several machines, both options scale, differently:

- With Option 1 you already have distinct names for free —
  `https://laptop.<tailnet>.ts.net`, `https://desktop.<tailnet>.ts.net`.
  Nothing more to do.
- With Option 2, advertise a differently-named service on each host:
  `svc:blanket-laptop`, `svc:blanket-desktop`. Each needs its own
  `autoApprovers` entry and its own grant, and each host needs to be
  tagged.

Note that several hosts advertising the *same* service name is a
different feature — that is high availability, and Tailscale will steer
a client to one of them. That is not what you want for blanket, where
each instance has its own queue and its own BoltDB; you would reach a
different task list depending on routing.

## Short names

The certificate is issued for the full `<name>.<tailnet>.ts.net`, so
that is the URL to use and bookmark. A bare `https://blanket` may
resolve through your MagicDNS search domain, but the certificate does
not carry that name, so expect a browser warning. Plain `http://` short
names do work — at the cost of the encryption you set this up for.

## Do not use Funnel

[Funnel](https://tailscale.com/kb/1223/funnel) exposes a service to the
*public internet*. Do not point it at blanket.

Blanket authenticates nobody. Behind a tailnet that is a deliberate
trade — the network already decided who you are. On the open internet it
means anyone who finds the hostname can submit a task type that runs a
shell command on your machine. The MCP interface makes that a single
well-formed request.

If you genuinely need blanket reachable from outside a tailnet, put a
real authenticating proxy in front of it and follow
[#101](https://github.com/turtlemonvh/blanket/issues/101).

## See also

- [MCP interface](../mcp.md) — why the default posture assumes a private
  network, and how to narrow it with `mcp.mode`.
- [Autostart on login/boot](../autostart.md) — keeping blanket running
  so the service it backs stays up.
