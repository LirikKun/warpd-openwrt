# Relay runtime integration — source candidate only

This guide describes the integrated **3.0.0 source candidate**, not a deployed
application, published release or live provider/tunnel acceptance. The README's
installation commands still select the previous published **v2.2.0** release;
they do not install this runtime. Supported release architectures remain Linux
`arm64` and `amd64`.

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
country endpoint. Bound DNS and the outbound UDP socket use the confirmed
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

## Evidence scope and remaining acceptance

The reviewed runtime baseline has two independent offline review passes
(lifecycle and API/policy), full Go and race runs with 764 passing tests each,
`go vet` and an arm64 build. A separate target-kernel diagnostic passed the
required kernel/socket mechanisms. The final runtime/UI candidate also passed
an independent UI review and combined Go/race runs with 764 passing tests each,
no failures or skips, plus `go vet` and a reproducible arm64 build. Its responsive
UI matrix covered 12 language/viewport/theme combinations. These are offline QA
results, not a deployed application or provider/tunnel demonstration. Integration
preserved the approved code bytes; only documentation changed afterward.

Before release/deployment acceptance, authorized live work still needs to
establish provider credential/address/Reserved/AWG compatibility, the intended
source path for observation, activation and UDP transport, actual tunnel health
and independent foreign egress through the exact current child/configuration.
It must also exercise runtime revocation/recovery and service persistence on
the installed candidate. A provider HTTP 200 remains **unverified activation**,
not binding confirmation. Neither local fixtures, user acknowledgement nor
HTTP IP observation alone proves binding or source-path equivalence. No live
provider request, registration, deployment or release is authorized by this guide.
