# Relay-WARP credentials and source-path implementation plan

Status: historical implementation plan, not shipped support. Profile-owned credentials and the single-child bridge are now integrated in the source candidate; current implementation and evidence scope are in the [Relay runtime integration guide](relay-runtime-integration.md). The investigation and pending-work wording below records the earlier stage, not current runtime availability. This plan does not authorize provider activation, account registration during development, deployment, or use of any key posted in chat.

## Credential modes

1. Generate a new WARP account for this Relay-WARP profile, only on an explicit, confirmed operation initiated by the user. Do not rotate, overwrite or register against the shared standalone WARP account. Configuration save, daemon restart, periodic monitoring and enabling an existing profile must never silently register accounts. Repeated/concurrent submissions must coalesce or be rejected while registration is in progress.
2. Manually enter an existing tunnel private key and the peer/server public key. The public-key label must explicitly mean the `[Peer] PublicKey`, not the client's public key derived from its private key. Allow manual tunnel addresses and necessary compatible parameters; a key pair alone is not proof of the account's assigned addresses or registration. Never invent IPv6 allocation or borrow credentials/Reserved from the existing shared account.

The pasted chat example is structural input only. Do not store its private key, addresses or opaque I1 packet in source, tests, logs or research artifacts. Use synthetic material for all offline tests. Do not request any replacement private key in chat; manual credentials belong in the protected router UI or a private local import workflow.

## Ownership and privacy

- Private tunnel credentials belong to the profile, separate from the Relay access resource and its activation URL. Multiple profiles may share activation state without sharing tunnel identity implicitly.
- Keep profile credentials in the private store with atomic persistence and the same strict filesystem protections as activation URLs. Preserve existing account/UCI bytes on create, edit, failure, upgrade and uninstall; purge remains explicit and allowlisted.
- Request credential fields are write-only. Public DTOs expose credential mode/configured/in-flight/error state only, never keys or a credential-derived public fingerprint. The UI clears submitted private material without repopulating it from status; polls cannot replace unsaved user input.
- Redact all scanner errors/output and generated configuration paths from public diagnostics as necessary. Never pass keys or activation tokens through process arguments. Generated credential-bearing engine YAML remains private RAM state.
- Credential replacement invalidates the applied configuration, candidate health and public authorization. Failed persistence must leave previous bytes/revision untouched; persisted-but-unapplied changes must close access authorized for the previous identity.

## Source binding and verification

The existing Relay client binds HTTPS observation/activation and DNS to a source IP, device and address family. Pinned Mihomo has interface/family options but no equivalent configurable local source-IP field. Do not silently drop the source-IP requirement.

Investigate an application-owned, loopback-only UDP bridge: Mihomo sends encrypted AWG datagrams to a private local endpoint; the bridge resolves and contacts the explicitly selected provider endpoint using the same source/device/family policy as activation. A namespace-isolated offline experiment has now demonstrated actual pinned-Mihomo AWG initialization and handshake-initiation forwarding through this mechanism, using synthetic data only. Parent verification matched all 21 evidence hashes and counted 15 passing race-enabled test executions in `/home/lirikdev/.hermes/cache/scratch/warpd-relay-udp-spike/`. This establishes transport feasibility, not a completed WireGuard handshake, provider compatibility, or approved production architecture. A separate isolated production-candidate module is being developed; the prototype's first-sender trust is explicitly not acceptable for integration. Its ingress must authenticate exact managed-child socket ownership, its lifetime must be bound to the applied configuration/child, and its state, datagrams, queues and cleanup must be bounded. Pin reply peers, invalidate expired DNS/path state, and never fall back to unbound sockets or direct WARP.

Retain independent proof layers: configuration parsing, actual AWG/I1 initialization, encrypted transport, candidate HTTPS health, configured exit policy, activation response and provider binding. `mihomo -t` does not execute lazy `device.IpcSet`; it cannot certify I1 initialization. An HTTP200 activation remains unverified. Only a fresh current-generation candidate probe may authorize the public TCP gate, without inventing provider binding confirmation.

## Acceptance additions

- Two profiles with distinct generated/manual synthetic credentials; no shared-account modification.
- Strict key encoding/length and tunnel-address validation; reject unsupported import fields/hooks and unsafe AWG tags without disclosing supplied values.
- Registration only through an explicit confirmed action; cancellation, concurrency, failure and restart never cause duplicate registration or account replacement.
- Public DTO/log/error redaction and write-only UI behavior in EN/RU, including failed saves and newer in-flight drafts.
- Real pinned-engine initialization in a network-isolated fixture, distinct from YAML `-t` checks.
- Exact-source/device/family forwarding, bound DNS failure, changed path/endpoint/credential, child exit, existing-stream revocation, expiry and no fallback.
- Full test/race/vet, packaging preservation, browser acceptance and fresh independent review before any deployment or publication.
