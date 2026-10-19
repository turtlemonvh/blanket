# Exposing blanket with Tailscale

[Tailscale Services](https://tailscale.com/docs/features/tailscale-services)
give blanket its own name on your tailnet — `https://blanket.<tailnet>.ts.net/`
— with a real HTTPS certificate, independent of which machine hosts it.
This is the approach we recommend. There are other ways to put blanket on
a tailnet (see [Alternatives and FAQ](#alternatives-and-faq)), but this one
gives a stable URL and keeps access control in one place.

Read [Security](#security) before exposing blanket anywhere: blanket has
no authentication of its own, so the tailnet is the access control.

## Before you start

- **Tailscale v1.86.0 or later** on the machine running blanket.
- In the Tailscale admin console, enable **MagicDNS** and **HTTPS
  certificates** for your tailnet.
- In the admin console's **Machines** page, give the blanket host a tag
  (for example `tag:server`). Services are hosted by tagged devices.
- Confirm blanket is up locally:

  ```bash
  curl -s localhost:8773/ >/dev/null && echo ok
  ```

## 1. Advertise the service

On the machine running blanket:

```bash
tailscale serve --service=svc:blanket --https=443 127.0.0.1:8773
```

That one command configures and advertises the endpoint.

## 2. Approve it

In the admin console, open the **Services** page and approve
`svc:blanket`.

## 3. Grant access

Nothing reaches the service until an access rule allows it. In the admin
console, open **Access controls** and add a rule in the visual editor:

- **Source:** the users or devices that should reach blanket — all
  members of your tailnet, or just yourself.
- **Destination:** `svc:blanket`
- **Port:** `443`

Blanket is now at `https://blanket.<tailnet>.ts.net/`.

To stop or remove the service later, see Tailscale's
[Services documentation](https://tailscale.com/docs/features/tailscale-services).

## Alternatives and FAQ

### I run blanket on several machines. How do I tell them apart?

Advertise a differently-named service on each host — `svc:blanket-laptop`,
`svc:blanket-desktop` — and approve and grant each one as above.

Don't advertise the *same* service name from several hosts. That is
Tailscale's high-availability feature: it steers each client to one of
the hosts. Each blanket instance has its own queue and its own BoltDB, so
you would see a different task list depending on routing.

### Can I skip the tag and the service?

Yes. `tailscale serve` without `--service` serves blanket under the
device's own name, on a device signed in as you, with no tag, approval,
or access rule to set up:

```bash
tailscale serve --bg --https=443 localhost:8773
```

Blanket is then at `https://<device>.<tailnet>.ts.net/`. The trade-off is
that the URL is tied to the machine — moving blanket means a new URL.

### Why does tagging the host matter?

A tagged device is owned by the tag rather than by the user who signed it
in. For a single-member tailnet that makes no practical difference. In a
shared tailnet, check who owns the tag before tagging a personal machine.

### Can I use a short name like `https://blanket`?

Not over HTTPS. The certificate is issued for the full
`<name>.<tailnet>.ts.net`, so that is the URL to use and bookmark. A bare
`https://blanket` may resolve through MagicDNS, but the certificate does
not carry that name, so expect a browser warning.

## Security

Blanket has no authentication. `POST /task/` will run whatever a caller
asks it to run, and the MCP interface can author a task type and launch a
worker for it — arbitrary code execution as the blanket user, by design
(see [MCP interface](../mcp.md#security--read-this-first) and
[issue #101](https://github.com/turtlemonvh/blanket/issues/101)).

That makes the tailnet the access control layer:

- **The access rule is your entire authorization model.** Write it as
  narrowly as you can live with. "All members" is every user in the
  tailnet.
- **Do not use [Funnel](https://tailscale.com/kb/1223/funnel).** Funnel
  exposes a service to the public internet, where anyone who finds the
  hostname can submit a task that runs a shell command on your machine.
  If you genuinely need blanket reachable from outside a tailnet, put a
  real authenticating proxy in front of it and follow
  [#101](https://github.com/turtlemonvh/blanket/issues/101).
- **Blanket listens on every interface.** There is no `bindAddress`
  option yet ([#44](https://github.com/turtlemonvh/blanket/issues/44)).
  Tailscale reaches it over loopback, but on a machine that is also on an
  untrusted LAN, port 8773 is open there too.

## See also

- [MCP interface](../mcp.md) — why the default posture assumes a private
  network, and how to narrow it with `mcp.mode`.
- [Autostart on login/boot](../autostart.md) — keeping blanket running
  so the service it backs stays up.
