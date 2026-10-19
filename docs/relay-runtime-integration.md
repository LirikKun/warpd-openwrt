# Relay runtime integration — v3.0.0

This guide describes the **v3.0.0** credential/runtime path. Use the current
[English README](../README.md) or [Russian README](../README.ru.md) for installation
and the [bilingual release notes](releases/v3.0.0.md) for upgrade highlights.
Release bundles target Linux `arm64` and `amd64`; the bounded live result below
covers only ARM64 OpenWrt on Filogic 820. An installed service, successful
activation request and working public tunnel are separate outcomes.

## Credential ownership and explicit actions

- Each `relay-warp` profile owns its tunnel credentials in the private profile
  store. The Relay resource owns activation access and monitoring state; several
  profiles can reference it without implicitly sharing tunnel credentials.
  The common standalone WARP account is not replaced or borrowed.
- Manual input uses `mode: manual`, `private_key`, `peer_public_key`, assigned
  `tunnel_ipv4`, optional assigned `tunnel_ipv6`, and optional compatible
  `reserved`, `dns`, `mtu`. `peer_public_key` is the server's `[Peer] PublicKey`,
  **not** the client's derived public key. Validation cannot prove assignment;
  obtain addresses from the actual account, never invent an IPv6 allocation.
- `PUT /api/profiles/{id}/account` stores validated manual credentials.
  `POST /api/profiles/{id}/account/generate` requires `confirmed: true` and the
  current nonzero configuration `revision`; it starts a bounded registration
  for that profile. Duplicate work is coalesced or rejected. Cancellation,
  stale configuration and restart do not silently resume registration.
  `DELETE /api/profiles/{id}/account/generate` cancels pending work.
- Credential input is write-only. `GET /api/profiles/{id}/account` returns safe
  mode/configured/job state, not keys, addresses or credential fingerprints.
  Successful storage/generation revokes old authorization; it is **not** an
  engine apply or activation action. Normal runtime reconciliation and fresh
  health checks are separate. Save, enable, restart and monitoring never
  implicitly register an account.
- Account-action `runtime_available: false` remains intentional metadata, not
  the runtime readiness signal. Use per-profile `/api/status` transport/policy
  readiness and `public_listening`; even an applied engine is not proof of
  readiness. Keep store backups and credential-bearing engine YAML private.

Sources: [credential API](../relay_account.go),
[generation lifecycle](../relay_account_generation.go),
[private persistence](../relay_account_store.go).

## Single-child transport and public authorization

The same managed Mihomo child serves ordinary and Relay profiles. Each Relay
profile uses an application-owned loopback UDP bridge to a selected allowlisted
country endpoint. Bound DNS and the outbound UDP socket use the acknowledged
source address/interface/address family; the engine's local endpoint is not a
direct-WARP fallback. Monitor-only Relay needs neither tunnel credentials nor
an engine, and monitoring/activation status is not tunnel proof.

Bridge authorization requires the exact current child, applied generation and
configuration identity, real `/proc` socket ownership and a retained socket
lease through Linux `pidfd_open`/`pidfd_getfd`. Kernel support and permission to
inspect/duplicate the child's descriptors are mandatory. Unsupported or denied
operations fail closed; no sampled-PID, unbound or direct-WARP fallback exists.
The private-listener bootstrap triggers lazy AWG initialization only; it is not
a completed handshake or tunnel-health result. Changes, child exit, ownership
loss and expiry revoke transport authorization; old bridges are closed/joined
before replacement generations are authorized.

A public gate requires the current applied configuration/generation and exact
child/listener ownership, a current Relay bridge, fresh successful transport
health and allowed observed HTTP-colo policy. Relay-WARP additionally requires
fresh **independent foreign-public-IP verification**, as does retained
`legacy-m2w`. A colo is not tunnel-peer identity. Missing, stale or unsuitable
evidence keeps the public port closed; revocation/expiry closes forwarded
streams too. Public SOCKS supports TCP CONNECT only, not UDP ASSOCIATE or BIND.
No routes, firewall rules, TUN or adjacent-service changes are introduced.

Sources: [engine integration](../relay_engine_runtime.go),
[UDP ownership and binding](../relay_udp.go), [health checks](../checks.go),
[foreign-egress policy](../geo_policy.go), [public gates](../profile_gate.go),
[status](../server.go). The [API working specification](profile-api-contract.md)
and [provider module contract](relay-provider-contract.md) retain earlier-stage
scope; this guide covers the later integrated credential/runtime path.

## Destination DNS

Relay destination hostnames resolve inside the tunnel (`remote-dns-resolve`),
using explicit profile credential DNS or, when absent, the same defaults as
standalone WARP: `1.1.1.1`, `1.0.0.1`. Stored credentials are not rewritten to add
defaults. A SOCKS hostname target is required: a literal Fake-IP supplied by a
client cannot be repaired by remote DNS. This destination policy is separate
from the source-bound outer Relay endpoint lookup and introduces no router DNS
listener, system DNS, routing, firewall or SSClash changes.

The peer has `0.0.0.0/0`; it additionally receives `::/0` only when validated
credentials contain an assigned tunnel IPv6 address. IPv6-only DNS without
assigned tunnel IPv6 is **unsupported**, not silently repaired by direct/system
fallback or an invented address. Offline engine/netstack checks are not live
IPv6-provider acceptance.

Source: [Mihomo profile renderer](../mihomo.go).

## Setup and safe shutdown

1. In **Relay → Add Relay**, save the private activation URL, intended source IP
   and/or interface, and the same-public-IP-path acknowledgement. Verify the
   intended egress yourself before acknowledging it; the checkbox is manual,
   **not automatic source-path or provider-binding proof**. Keep the resource
   disabled during preparation. Editing URL/source/interface requires renewed
   acknowledgement. Do not open the credential URL from another host.
2. In **Add proxy**, create a disabled Relay-WARP child linked to that resource,
   with the intended server country and its own public port. Server country is
   not a promised egress country.
3. Open the child's **Tunnel account · this profile only** disclosure. Explicitly
   import assigned credentials or confirm **Register a new account**, then wait
   for completion. This does not activate Relay, apply the engine or replace the
   shared WARP account. Do not replay an ambiguous generation request.
4. Enable the parent, then the child. Parent monitoring can send provider
   requests. Wait for fresh, generation-matched transport/policy readiness and
   `public_listening` in `/api/status`, not just `persisted` or `applied`.
5. Configure `socks5h://<router-LAN-IP>:<profile-port>` and verify proxied HTTPS
   with TLS certificate verification enabled. No implicit registration occurs
   on save, enable, restart or monitoring.
6. Disable the child **while its parent is still enabled**, then disable the
   parent; read back both desired flags and the closed public port. Current child
   toggles, including `enabled:false`, are blocked while the parent is off.
   Parent disablement pauses children without changing their saved choices;
   re-enabling it can resume children whose saved enabled flags remain true.

The activation credential is write-only private data; keep it and profile-store
backups out of logs, issues, screenshots and public reports. Delete every linked
child, including disabled children, before deleting a resource.

## Evidence scope

### Current verification

The reviewed runtime passed **800 full Go tests and 800 race tests**, `go vet`
and an ARM64 build. These are reused runtime results, not a new full-suite run
for this documentation update. See [README release verification](../README.md#release-verification).

On **2026-10-19**, bounded live acceptance on **ARM64 OpenWrt, Filogic 820**, with
**Mihomo 1.19.31 / WarpScout 0.16.0**, established:

- Fresh Relay-WARP health for the applied engine generation, policy readiness
  and a working public SOCKS gate.
- A LAN-client public-SOCKS request to `https://example.com` with **HTTP 200**,
  verified example body and **verified TLS certificate**; independent
  `ifconfig.co` geolocation reported **DE**.
- **One observed activation attempt and zero new account-generation actions**
  during final DNS acceptance. Explicit profile-account generation had passed
  earlier after support for the pinned generator's optional `outer` metadata;
  it was not repeated during final DNS acceptance.
- The system Fake-IP setup remained active; protected account material,
  SSClash configuration, router routes and firewall remained unchanged.
  Relay and its tested child were restored off, the public port closed,
  and ordinary WARP passed readiness and real SOCKS HTTPS checks.

Initial private startup probes failed before the later fresh checks and public
gate passed. Their failure is not hidden, and a successful early private probe
would not by itself authorize the public port either.

This proves one bounded provider/path/device result, **not** universal provider
support, live IPv6, an AMD64-router deployment, sustained stability or every
revocation/recovery and service-persistence scenario. Required kernel support
must still be available on each device. Keys, assigned addresses and compatible
Reserved/AWG parameters must come from the actual account/provider.

A provider HTTP 200 remains **unverified activation**, not independently
confirmed binding. Neither user acknowledgement nor an HTTP IP observation
alone proves equivalence of activation and UDP source paths. This guide does
not authorize additional provider requests or account registrations.

### Historical offline integration results

Earlier integration review recorded two independent lifecycle/API-policy
passes, 764 passing full Go tests and 764 race tests, `go vet`, an ARM64 build,
and a separate target-kernel mechanism diagnostic. The earlier runtime/UI
integration also passed independent UI review and a 12-combination
language/viewport/theme matrix. These historical results establish their own
offline scope, not live tunnel acceptance; the current result is above.
