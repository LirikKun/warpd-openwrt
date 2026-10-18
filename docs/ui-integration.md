# UI integration and verification

> Historical verification below covers the prior UI bytes only. The new isolated Relay credential slice and its fresh verification/limitations are documented at the end; prior browser evidence/approval does not extend to the new disclosure.

## Contract

The console polls `/api/status`: `revision`, `profiles`, `relays`, `proxies` keyed by stable profile ID, `defaults.awg`, and existing `config`, `scan`, `mihomo`, time/version fields. Missing/incomplete or stale status disables writes and removes green readiness. Lower server revisions and obsolete in-flight polls cannot overwrite newer state.

- CRUD uses POST/PATCH/DELETE `/api/profiles` and `/api/relays`; DELETE sends JSON `{}`. Inapplicable fields are omitted, never `null`. Profile type is immutable. Multiple same-type profiles and an intentionally empty list render without fixed slots.
- WARP/Relay-WARP AWG fields and reset use the real API preset, including its version and I1. MASQUE exposes QUIC/HTTP2 and omits AWG. Hidden incompatible AWG inputs do not block native form validation.
- Exclusions show effective union and global/local/system source per code. Public-IP country, HTTP-colo country, Relay server country and independent IP proof remain distinct. A Relay country label cannot make policy green.
- Legacy `legacy-m2w` rows are migration-only: transport/pin controls are read-only, matching the API. Rename/bind/filters/enabled/delete remain available; no new chain option exists.
- Dirty row/global/create drafts survive blur and polls, including edits typed during an in-flight save. Submitting one row never commits another. Normalized filter/name readback is confirmed semantically; AWG object key order is irrelevant.
- HTTP 503 with `persisted:true` displays saved intent separately from failed application. Switches follow authoritative readback, not optimistic clicks.
- Language is saved through `/api/config` `web_language`, with an explicit reload instruction (no automatic reload discarding drafts). The server-rendered first paint and production dynamic script are localized through `renderWebUI` for both languages.

## Public Relay API

`defaults.awg` and source-path acknowledgement invalidation are implemented in the parent API. UI clears the checkbox when source/interface/URL changes and sends `path_confirmed:false` unless the user explicitly rechecks it.

`POST /api/relays/{id}/activate` requests bounded/coalesced Relay monitor activation. UI requires enabled/configured/path_confirmed and an explicit localized source-path confirmation; request acceptance is never called tunnel readiness.

Safe monitor diagnostics come from the public Relay DTO's **`status: RelayMonitorStatus`**, not `runtime` or flattened Relay fields: `state`, `observed_ip`, `in_flight`, `activation_outcome`, `error`, `last_observation_at` (other safe monitor timestamps/proof fields may be included). State values include `waiting`, `debouncing`, `unverified`, `cooldown`, `disabled`, `configuration_blocked` and `applying`; activation outcomes are `unknown`, `error` or `unverified`, not invented `accepted`/`http_accepted_unverified` states. The UI displays these raw monitor diagnostics separately from child tunnel readiness, and conservatively labels the resource's tunnel/foreign IP unverified. `binding_verified` and `tunnel_verified` remain false in the current monitor contract. Missing `status` shows unavailable/not checked rather than falling back to a different interface or inventing readiness.

Relay URL inputs are password fields, never prefilled, cleared immediately after dispatch, and omitted when blank on edit. Known credentials are redacted from rendered diagnostics. Parent disable pauses child switches while retaining their own saved flags; child deletion remains available. Resource deletion still reaches backend dependency validation and displays its refusal. No activation URL is opened by this UI or the offline tests.

## Executed verification

Scratch dependencies only; no production npm dependency was added. Behavioral RED/GREEN logs: `~/.hermes/cache/scratch/warpd-redesign-ui/01-*` through `09-*`.

```sh
GO_BINARY=/home/lirikdev/go-toolchain/go/bin/go \
JSDOM_PATH=/home/lirikdev/.hermes/cache/scratch/warpd-ui-interaction/node_modules/jsdom \
node --test ui_interaction.test.cjs
# 24 passed, 0 failed; full production scripts, intercepted fetch only.

/home/lirikdev/go-toolchain/go/bin/go test \
  webui.go webui_i18n_test.go policy_ui_test.go geo_ui_test.go -count=1
# PASS. Existing geography/policy safety assertions retained.

/home/lirikdev/go-toolchain/go/bin/go test ./...
# PASS: ok warpd 119.013s at the integration snapshot tested.
```

The Node suite covers CRUD, monitor-only Relay, dependency pausing, write-only secrets, explicit path confirmation, AWG reset/edit, MASQUE selection, exclusions, stale/missing status, pending/double-click/poll/auth races, normalized authoritative readback, persisted-but-unapplied failures, localized first paint/ARIA/confirmations/dates, inert/redacted diagnostics, scans/progress/queue timeout, settings and account maintenance.

Browser fixture QA is now executed with native Windows Edge/CDP against production-rendered EN/RU HTML and explicitly intercepted synthetic API responses: 1366px desktop plus 360px/390px mobile, each in light/dark mode (12 combinations). All passed real mouse-dispatch switch/disclosure interactions, actual ≥44px switch/disclosure geometry, no horizontal overflow, single-line/unclipped IPv6 SOCKS and endpoint addresses, and zero captured console errors/exceptions. Expanded profile/AWG/MASQUE/Relay/global details and both create forms are captured, including full expanded-page PNGs. Evidence: `/home/lirikdev/.hermes/cache/scratch/warpd-relay-ui-qa/` (`01-status-red.log`, `02-status-green.log`, `03-renderer.log`, `04-browser.log`, `measurements.json`, `summary.json`, `screenshots/`). The .status contract regression failed in both locales before the minimal production fix; the complete Node suite still passes all 24 tests.

These are **offline UI fixtures**, not proof of live backend persistence, origin/CSP/cookie behavior, device listener ownership, activation, binding or proxy/provider acceptance. Live Relay end-to-end acceptance remains separate. No commit, deployment, router changes, live account registration or personal activation requests were made by the UI worker.

## New isolated Relay credential UI slice

Source: `warpd-relay-credentials-ui/source`, copied from frozen `core-repair-v1/source`. UI-owned changes are only `webui.html`, `webui.go`, `webui_i18n_test.go`, `ui_interaction.test.cjs` and this document. No backend implementation, main-repository edit, router/provider request or deployment. Preserve repaired FD store/migration/I1 code when combining this slice with the credential worker; its older source baseline is not a replacement core.

### Interface and invariants

- A compact disclosure inside each existing Relay-WARP row offers generation or manual input. WARP/MASQUE, shared standalone `/api/register` account maintenance, profile forms and their long-I1 limits remain unchanged.
- Manual mode requires private key, **server public key from `[Peer]`, not the derived client key**, and assigned IPv4; IPv6, Reserved, DNS and MTU are optional, never inferred or borrowed. Hidden manual inputs are disabled. Backend canonical-key/address validation remains authoritative; DOM tests use synthetic non-credential markers.
- Keys are password inputs with `new-password` autocomplete. Responses never repopulate keys, addresses, Reserved or DNS. Successful persistence clears only submitted values whose value **and edit version** still match (including protection against edit-away-and-back drafts). Failed saves and newer drafts survive blur, polling and readback. Entered secret fields join the existing in-memory diagnostic redaction set; no keys enter logs/status/toasts/URL/localStorage.
- Manual save **always** confirms replacement intent, even if cached metadata says unconfigured. It sends only the exact `PUT /api/profiles/{id}/account` DTO: `mode:manual`, required `private_key`, `peer_public_key`, `tunnel_ipv4`, plus nonempty optional `tunnel_ipv6`, `reserved`, `dns`, numeric `mtu`. Omitted optionals are cleared by backend replacement semantics. No AWG/profile fields enter account requests.
- `{persisted:true,applied:false,runtime_available:false}` means saved, not failed/rolled back. Matching submitted secrets clear, then the exact account endpoint is read back. A failed readback says **saved/status unavailable**, without resurrecting secrets. `credential_configured` is metadata, never tunnel/application/foreign-exit readiness. Relay runtime-unavailable wording stays intact.
- Generation first reads fresh `GET /api/profiles`, validates a positive safe revision and Relay profile, then asks localized destructive confirmation. Only this explicit action sends `POST /api/profiles/{id}/account/generate` with `{confirmed:true,revision}`. Save, enable, monitoring, activation and polling never register or automatically retry.
- `queued:false` means already pending, **not another successful queue**. Pending disables generation; manual save remains available (backend cancels pending generation). Cancellation sends `DELETE /api/profiles/{id}/account/generate` with `{}`; it does not delete existing credentials and warns that provider-side registration might already have happened.
- Poll `GET /api/profiles/{id}/account` while disclosure is open or generation is pending. Retain only allowlisted enum/pending/mode/configured values. All six states (`idle`, `pending`, `succeeded`, `error`, `cancelled`, `stale`) have localized messages. Epoch/sequence checks discard pre-mutation/cancel reads. Successful background reads do not erase failed actions. Missing old-profile metadata means unconfigured; malformed account DTOs fail safely. Arbitrary backend errors never become account UI text/HTML; existing auth/origin guidance remains localized.
- Credential body limit is the backend's 16,384 bytes; generic profile POST/PATCH budget remains separately 524,288 bytes. This slice does not add arbitrary form maxlength restrictions.

### Fresh execution and DTO coverage

```sh
GO_BINARY=/home/lirikdev/go-toolchain/go/bin/go \
JSDOM_PATH=/home/lirikdev/.hermes/cache/scratch/warpd-ui-interaction/node_modules/jsdom \
WARPD_ACCOUNT_DTO_FIXTURE=../backend-dto-fixture.json \
node --test ui_interaction.test.cjs
# 37 passed, 0 failed, 0 skipped; includes all original 24 tests.

GOTOOLCHAIN=local /home/lirikdev/go-toolchain/go/bin/go test -count=1 ./...
GOTOOLCHAIN=local /home/lirikdev/go-toolchain/go/bin/go test -race -count=1 -run WebUI .
GOTOOLCHAIN=local /home/lirikdev/go-toolchain/go/bin/go vet ./...
```

Full production JS and real EN/RU Go renderer are exercised, intercepting only fetch. New tests cover exact payloads/omitted optionals, passwords/write-only readback, required/hidden controls, localized destructive confirmations, fresh revision, queued:false, six states, cancellation/stale-poll races, readback failure, old metadata, newer/ABA drafts, auth/origin/rejection matrices, and inert diagnostics.

`../backend-dto-fixture.json` is emitted by **actual backend HTTP handlers/serializers** in the additional disposable `../dto-probe` copy using `ui_dto_evidence_test.go`; the credential worker source was not edited. It contains real profile listings before/after a synthetic manual save, account GET/save responses and six serialized status shapes. No generation command is invoked. Supplying `WARPD_ACCOUNT_DTO_FIXTURE` enables the additional production-DOM compatibility test against these real DTOs; without it the self-contained contract tests still run (36 tests). Reproduce:

```sh
(cd ../dto-probe && WARPD_UI_ACCOUNT_FIXTURE=../backend-dto-fixture.json \
 GOTOOLCHAIN=local /home/lirikdev/go-toolchain/go/bin/go test -count=1 -run '^TestUICredentialDTOEvidence$' .)
```

Evidence under `warpd-relay-credentials-ui/`: `initial-hashes.json`, `final-hashes.json`, `ui.patch`, `delta-summary.json`, RED/GREEN logs, final DOM/Go/race/vet logs, DTO producer and fixture. These new bytes require fresh combined-source review; prior `c359...` UI approval and historical browser matrix do not cover them. Parent integration must rerun full combined backend/race tests and desktop/mobile/light/dark browser acceptance. jsdom does not prove layout, real CSP/origin enforcement, provider assignment, tunnel readiness or foreign exit.
