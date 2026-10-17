# Offline UI interaction tests

The production console is still one Go-embedded `webui.html`, with no bundler,
CDN, external fonts, or JavaScript runtime dependency on the router.

Run from the repository root with Node 20+ and jsdom installed **outside the
repository**:

```sh
npm install --prefix "${TMPDIR:?}/warpd-ui-interaction" --no-audit --no-fund jsdom@26
JSDOM_PATH="$TMPDIR/warpd-ui-interaction/node_modules/jsdom" node --test ui_interaction.test.cjs
```

On this WSL host Node is available at
`/home/lirikdev/.hermes/tools/node-26.7.0-linux-x64/bin/node`.
Put its directory on `PATH` for npm if needed.

These tests execute the actual embedded page JavaScript in jsdom. All HTTP is
intercepted in the DOM's fetch boundary; they cannot contact a router. Fixtures
use reserved documentation IPs, generated timestamps, and generated test-only
authentication values. No credentials are stored in fixtures or snapshots.

Coverage includes four exact `/api/proxy` contracts, server-authoritative
readback, HTTP/network/apply failures, pending and stale polling races, initial
and disconnected controls, auth recovery without DOM destruction, legacy-query
removal, inert server text, SOCKS copying, row-local endpoint submissions and
M2W pinning, dirty settings across blur/poll/save, confirmation of destructive
shared-engine/account actions, rejected/queued scans, progress and scan locks,
and fail-closed policy with separate colo/IP geography.

The implementation was developed with observed failing tests before the
corresponding changes; the final suite passes 22 tests. jsdom does not measure
layout or contrast. Real-browser review should cover light/dark preference,
390px and narrower layouts, touch target/focus states, details open/close,
clipboard availability on insecure LAN HTTP, and cancellation of confirmations.
The console handles clipboard denial with a manual-copy message.

## UI contract and safety notes

- Switches represent `proxies[key].enabled` (desired configuration), not a claim
  that the engine is applied or its network/policy checks succeeded. While a
  write/readback is pending, controls are locked and readiness is suppressed.
- A failed apply is displayed inline even when the desired flag persisted.
  Actual readiness requires a fresh server transport check and policy approval.
  An older request cannot overwrite post-mutation status or a new token session.
- Settings only submit dirty fields. Values remain dirty after blur and failed
  requests, and clear only after matching server readback. Changes made during
  save are retained.
- `/api/endpoint` always receives exactly one row. M2W sends `outer` and `inner`.
  Endpoint forms preserve local drafts and do not apply sibling open forms.
- Endpoint/restart/account endpoints can acknowledge asynchronous work; copy
  says the request was accepted, never that background work completed.
- Scan `queued:false` is rejection, not success. `queued:true` stays visibly
  queued until server scanning/completion is observed; unconfirmed start times
  out visibly. Shared-config mutations are conservatively locked during scans.
- Global restart uses `{proxy:"all"}` and is explicitly labelled as shared
  mihomo; rotation and new-account actions are tucked under maintenance.
- The current status schema has no authoritative applied-enabled flag. Details
  therefore identify the desired flag and say application is evaluated through
  network checks. Optional `applied_enabled` is displayed only if supplied.
- Foreign-IP proof is required specifically for M2W. An RU public-IP country
  alone does not reject standalone services with an allowed observed colo.
  Unknown/RU/excluded colo and DME/SVO never get green policy readiness.
- Fail-closed readiness is an assessment, not traffic enforcement. The console
  explicitly warns that an unsuitable SOCKS listener may still carry traffic.
