# Core integration outcome

> Historical core-stage report, not current Relay runtime status. The Relay-unavailable findings below describe the earlier integration stage; they are superseded by the [integrated Relay runtime guide](relay-runtime-integration.md). Historical test results are scoped to that stage, not evidence of live provider/tunnel acceptance.

## Implemented and integrated

- Contract model/store helpers in `profile_store.go`, `profile_validation.go`, `profile_runtime.go`; private atomic JSON persistence, deep snapshots, internally serialized transactions, revision publication only after successful persistence. Existing empty stores remain empty. Corrupt/future/invalid stores are preserved and rejected; symlink/special/hardlinked files and symlink ancestors are rejected. Store parent is tightened to 0700; file to 0600.
- UCI remains unchanged on migration. Existing sections (including disabled profiles) migrate; fresh `global.profile_schema=1` creates only WARP. Legacy M2W is migration-only: retained, deletable, name/Enabled editable, tunnel fields read-only. New chains rejected. Unpinned migrated-chain dependencies scan even without standalone profiles.
- Dynamic renderer/scans/checks use stable IDs and independent endpoint/health state. Effective exclusions union builtin DME/RU + global + local. Late dynamic scans/probes reject changed settings, parent resources and effective global/local rules, not merely rendered YAML. Existing legacy fixture behavior remains covered.
- Real data-plane exclusion enforcement: engine candidates bind private loopback SOCKS ports; app-owned public TCP gates open only after current applied-generation + endpoint + policy verification. Revocation closes listener AND accepted streams; expiry closes stale authorizations. SOCKS CONNECT only: BIND and UDP ASSOCIATE are deliberately unsupported, preventing an ungated UDP escape. No routes/firewall/TUN changes.
- `main.go` initializes profiles once, retains management on store failure with ProfileError and blocked runtime/scans, starts independent Relay monitoring via `srv.StartRelays(ctx)`, closes gates on shutdown. Version is 3.0.0.
- By parent permission, only `liveEndpoint` wrapper and status-runtime identity fields were targeted-patched in `server.go`: dynamic status includes ID/type/paused/effective_enabled/public_listening; dynamic Ready requires a public gate; checked-policy identity prevents stale green. Root `profile_error` is included.

## AWG compatibility

Persisted preset: `warpscout-0.16.0-mihomo-1.19.31-awg-v1`.

JC/JMin/JMax/S1/S2 are int, H1..H4 uint32, I1 string. JC 1..128; sizes bounded to 1280; validated allowlisted I1 tag chain. Supported edits JC/JMin/JMax/I1 reach both scanner arguments and engine YAML. S1/S2=0 and H1..H4=1,2,3,4 remain fixed and incompatible edits are rejected: pinned WarpScout exposes no custom S/H flags. MASQUE rejects non-default AWG options. Sources checked: WarpScout v0.16.0 flags.go/warp.go/wgconf.go and Mihomo v1.19.31 wireguard.go.

Real cached **Mihomo v1.19.31** ran `-t` on production-rendered two-profile AWG YAML successfully; no network tunnel was activated. Reproduce with `WARPD_TEST_PINNED_MIHOMO=<cached-amd64-mihomo> go test -run '^TestAWGPinned' -v .`. Cached WarpScout `scan -h` also executed offline, confirming flags.

## Verified locally

Evidence: `~/.hermes/cache/scratch/warpd-redesign-core/`.

- Baseline `go test ./...` passed before edits.
- Behavioral RED/GREEN logs cover store lifecycle, migration, validation/dependencies/failed persistence, concurrent transactions, filesystem safety, dynamic renderer/filters/scans, private-probe integration, real gate forwarding/revocation/expiry, chain-only migration and unsupported Relay behavior.
- Final `GOTOOLCHAIN=local ~/go-toolchain/go/bin/go test -race -count=2 ./...`: **passed** (`race-final.log`).
- `go vet ./...`: **passed** (`vet.log`). `git diff --check`: **passed**.
- linux/arm64 and linux/amd64 static builds succeeded in scratch; amd64 executable returned `warpd 3.0.0`.

## Explicit remaining limits / parent follow-ups

1. **Relay-WARP tunnel output is NOT implemented/verified against the provider configuration.** Persisted dependency-safe resources/profiles and independent monitoring are supported, but enabled Relay-WARP remains unavailable with an explicit compatibility error and NO listener, NO direct-WARP fallback. A Relay-only unsupported configuration does not load a WARP account/start Mihomo. Actual provider peer keys, tunnel addresses, Reserved/AWG compatibility and source-IP/interface/address-family equivalence need authorized router-side evidence before implementing/claiming that path. Neither ack nor activation HTTP200 establishes it.
2. Migration captures the legacy engine's actual I1 from the adjacent private `warp-account.json`, retaining the old generator label as metadata. If a legacy `gen_i1` protocol label has no captured valid account I1, migration stops visibly instead of inventing a packet or dropping the original UCI/account data. No account registration/rotation was attempted.
3. Public gates are TCP SOCKS CONNECT only; document UDP/BIND restriction. Readiness after runtime edits is refreshed by the configured health-check round, not an immediate new probe inside CRUD.
4. Parent/UI should surface root `profile_error`, the Relay compatibility error and separate `public_listening`/transport/policy readiness. Keep Relay provider support advertised as unverified until live acceptance. Review/deployment/publication remain parent-owned; no router/WAN/SSClash actions, commits or push were performed.
