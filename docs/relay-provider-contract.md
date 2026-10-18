# Relay provider contract and integration boundary

## Evidence and remaining blocker

Only public repository content was fetched. No personal activation URL, Telegram
session, provider activation endpoint, live account registration or router was
accessed. All activation/IP-observation tests terminate at local fixtures.

Public sources inspected:

1. [`openwarpkit/warp-relay`, commit
   `a1ef6ba3d3ad44ef8f79508d2b9e22528197323c`](https://github.com/openwarpkit/warp-relay/tree/a1ef6ba3d3ad44ef8f79508d2b9e22528197323c),
   especially [`simple.sh`](https://github.com/openwarpkit/warp-relay/blob/a1ef6ba3d3ad44ef8f79508d2b9e22528197323c/simple.sh)
   and the README. This is server-side UDP forwarding using DNAT/masquerade.
   The example forwards UDP port 4500 to `engage.cloudflareclient.com:4500`.
   It does not terminate WireGuard, issue client keys, or implement the personal
   activation HTTP API. These server setup scripts are **not** executed by warpd.
2. [`nellimonix/base-relay`, commit
   `3ede6508496a5183140d85c262d53b4d50a304da`](https://github.com/nellimonix/base-relay/tree/3ede6508496a5183140d85c262d53b4d50a304da),
   especially [`src/proxy.js`](https://github.com/nellimonix/base-relay/blob/3ede6508496a5183140d85c262d53b4d50a304da/src/proxy.js).
   This is an HTTP reverse proxy for Vercel/Netlify/Cloudflare Workers using a
   static `BASE_URL`. It forwards application methods/headers/body and returns
   upstream redirects without following them. It is not the UDP relay or an
   implementation/specification of `/activate/<token>`.
3. Corroborating, **not authoritative**: the public
   [`StressOzz/Mixomo-Manager/gen_WARP.sh`](https://github.com/StressOzz/Mixomo-Manager/blob/main/gen_WARP.sh)
   lists commented tribukvy Relay endpoints while obtaining peer key and tunnel
   addresses from a normal WARP account. Commented endpoints and third-party
   client code do not establish current provider support or activation semantics.

**Unresolved provider-specific contract:** none of these sources specifies the
response schema, binding confirmation/readback, authorization failures, expiry,
rate limits, or authoritative country endpoint/configuration parameters for
`https://relay.tribukvy.ltd/activate/<token>`. The module treats opening the
configured activation link as a bounded GET, but no live result has validated
that method or any response interpretation. `200`, including arbitrary JSON
containing `success: true`, means **unverified**, never confirmed activation.
No parser invents a confirmed IP from arbitrary provider output.

For a transparent UDP forwarder, retaining a valid WARP private key, peer public
key, assigned addresses and Reserved bytes while changing only the UDP endpoint
is compatible **in principle**: the forwarder changes packet addressing, not the
WireGuard cryptographic identity. It does **not** prove this provider uses that
implementation or that the user's account, Reserved/AWG parameters and selected
Relay endpoint are compatible. Do not import or manufacture alternate credentials,
change peer keys/addresses/Reserved/AWG, silently fall back to direct WARP, or
claim a working foreign exit based on this research. A provider specification or
explicitly authorized router-origin live test plus independent tunnel/exit proof
remains necessary. Treat production automation as experimental until that
contract is supplied; source-path acknowledgement is not provider verification.

## Standalone Go API

All types are independent of `Settings`, `RelaySettings`, account registration
and the engine. Files are `relay_client.go` and `relay_monitor.go`, package main.

```go
// Private input; every field has json:"-"; String/GoString redact it.
type RelayAccess struct {
    URL, Interface, SourceIP string
    PathConfirmed bool
}

func ValidateRelayActivationURL(raw string) error

// Zero value: system TLS trust, no environment proxies, bound direct sockets.
type RelayClient struct { /* private local-test transport seam only */ }
func (*RelayClient) Observe(context.Context, RelayAccess) (RelayObservation, error)
func (*RelayClient) Activate(context.Context, RelayAccess) (RelayActivation, error)

func NewRelayMonitor(*RelayClient, RelayMonitorOptions) *RelayMonitor
func (*RelayMonitor) Configure(id string, access RelayAccess, enabled bool) error
func (*RelayMonitor) Reconcile(context.Context, id string, manual bool) RelayMonitorStatus
func (*RelayMonitor) Status(id string) RelayMonitorStatus
func (*RelayMonitor) Remove(id string)
func (*RelayMonitor) Close()
```

Use **one daemon-wide monitor**, keyed by stable Relay resource ID, shared by all
referencing profiles. `Configure` rejects duplicate nonempty activation URLs
under different resource IDs, even when disabled. It also reserves the identity
of a canceled but still completing operation. Validate the same uniqueness before
persistence in the integration layer. Multiple country profiles should reference
one resource, not create duplicate activation resources.

Map `RelaySettings.ActivationURL/Interface/SourceIP/PathConfirmed` to `RelayAccess`
and call `Configure` for all desired resources after private persistence, before
starting/restarting children. Repeated identical Configure calls are no-ops.
Configure changes/disablement cancel in-flight work and reject stale results.
Remove cancels/deletes runtime state; Close permanently stops the monitor and
cancels work. These calls do not wait for the remote server to acknowledge
cancellation, and local disablement cannot revoke a request already processed
by the provider.

Schedule `Reconcile` independently of account availability, SOCKS health, and
engine state, once per resource at its configured monitoring interval. It is a
synchronous bounded call: run different resources concurrently from the owner's
supervisor; do not hold the application's global apply lock across network I/O.
Concurrent calls for the same resource return an in-flight snapshot without
launching duplicate work. The monitor itself starts no goroutines or timers.
For the manual activation endpoint call `Reconcile(ctx, id, true)`, **not** the
low-level `Activate`: Reconcile enforces fresh IP observation, debounce and
cooldown. A returned `debouncing`/`cooldown` state or an `in_flight` flag is not successful
activation and does not enqueue an undisclosed future request. The owner must
schedule later reconciliations. Account registration and engine startup are not
monitor prerequisites.

Default monitor options: debounce 10 seconds; cooldown 60 seconds; retry base
30 seconds; maximum three failed activation attempts per stable IP. Backoff is
`max(cooldown, retryBase * 2^(attempt-1))`. Debounce and retry base are capped at
one hour; cooldown at 24 hours; attempts at three. Separate ticks perform retries;
there are no nested transport retries or sleeps. Unknown/invalid IP resets
stabilization and never activates. A different IP must stabilize again and cannot
bypass resource cooldown. An ambiguous 200 is attempted once for an unchanged IP;
it is not repeatedly rebound on every periodic check. Manual requests can retry
an attempted/exhausted stable IP only after debounce/cooldown. Cooldown survives
configuration edits and disable/re-enable within the running daemon. Runtime
state is in memory, not durable across daemon restarts.

`RelayMonitorStatus` is safe to serialize. It separates current `observed_ip`,
`last_observation_at`, `last_attempt_ip`, `last_attempt_at`, `next_attempt_at`,
`activation_outcome`, `state`, generic `error`, and `in_flight`. Outcomes are
`unknown`, `error`, `unverified`. `binding_verified` and `tunnel_verified` are
always false with the currently unconfirmed protocol. Do not derive proxy
readiness, geographical acceptance, or a confirmed binding from these fields.
Parent/child listener gating, dependency validation, persistence and API auth
remain the integrator's responsibility.

## Request/source security

- URL validation accepts only canonical HTTPS origin `relay.tribukvy.ltd`, no
  userinfo/explicit port/query/fragment, and exact `/activate/` plus a 1–256
  character ASCII alphanumeric/underscore/hyphen token. Encoded paths, extra
  segments and alternate hosts are rejected before transport construction.
- Requests originate in the daemon on the router, not the management browser.
  They require `PathConfirmed` and at least a source interface or local source
  address. SourceIP means the router's **local** address, not its NAT public IP.
- Linux `SO_BINDTODEVICE` binds the requested interface; `net.Dialer.LocalAddr`
  binds the address. DNS sockets use the same device/address/family. IPv4 is
  explicit when no source address is specified; an IPv6 source selects IPv6.
  Missing interface, address mismatch, privilege failure or inaccessible bound
  DNS resolver fails closed. There is no unbound fallback. A loopback-only DNS
  forwarder may therefore be unreachable from a WAN-bound configuration.
- Public IP observation uses fixed HTTPS `https://api64.ipify.org/`, a 64-byte
  body budget, and matching address-family validation. Invalid/private/loopback,
  link-local, multicast, unspecified, carrier-NAT and selected reserved IPv4
  addresses are unknown, not an IP change. Observation only discloses ordinary
  egress IP to that service; no activation token is sent there.
- `http.Transport.Proxy` is nil, never `ProxyFromEnvironment`; every redirect is
  returned as an error without following it, including same-origin redirects.
  TLS certificate/hostname verification is never disabled. Production does not
  expose a URL allowlist override, custom CA override, or insecure test mode.
- Each HTTP operation has a 15-second total context/client limit, 5-second
  connect/TLS/header limits, 16-KiB response header budget and 64-KiB activation
  body budget. Reconcile adds one 30-second overall deadline for observation plus
  activation. Caller deadlines/cancellation win. Compression and keepalive are
  disabled; bodies are not logged or interpreted as credentials/configuration.
- Errors never include request URLs, response bodies, underlying transport
  errors, redirect targets, or malformed peer fields. The monitor stores no
  provider response body. Keep the original URL in the private store only;
  never send it in status, exports, shell arguments or logs.

Binding does not bypass or change transparent proxy/firewall rules. Confirm the
intended router egress for observation, activation **and** Relay transport using
read-only path evidence before acknowledging `PathConfirmed`. A monitor cannot
prove that a separately configured engine's UDP sockets use that same path.
No router network, firewall, routing or adjacent-service settings are changed.

## Offline verification

Explicit-file tests avoid concurrently edited unrelated package files:

```sh
GOTOOLCHAIN=local /home/lirikdev/go-toolchain/go/bin/go test -race -count=3 \
  relay_client.go relay_monitor.go relay_client_test.go \
  relay_client_tls_test.go relay_monitor_test.go
GOTOOLCHAIN=local /home/lirikdev/go-toolchain/go/bin/go vet \
  relay_client.go relay_monitor.go relay_client_test.go \
  relay_client_tls_test.go relay_monitor_test.go
```

Tests cover stable/change/unknown/wrong-family IP, bounded retries and cooldown,
manual requests, ambiguous success, duplicate resources, coalescing, unrelated
resource progress, cancellation and late results on disable/edit/remove/close,
URL/error redaction, TLS trust, proxy bypass, redirects, header/informational/body
bounds, exact chunked-body budgets and stalled-body deadlines. Local TLS fixtures
retain production hostname/SNI/path and inject only fixture roots and loopback
dialing. Behavioral RED/GREEN evidence is outside the repository under
`~/.hermes/cache/scratch/warpd-redesign-relay/`.

These are offline module checks, not provider activation acceptance, complete
application integration, deployment, router source-path proof or tunnel proof.
The parent integration must run the complete repository test/race/vet/build gates.
