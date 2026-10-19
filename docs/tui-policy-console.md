# TUI console and per-profile policy (v3.1.0)

This guide describes the v3.1.0 console and per-profile policy; the installer targets v3.1.0. See the [release page](https://github.com/LirikKun/warpd-openwrt/releases/tag/v3.1.0) for published availability, the [release notes](releases/v3.1.0.md) and the [current guide](../README.md) for setup. Source review and package verification do not establish that a router has been upgraded or that a live provider tunnel has been tested.

## Console

- Four sections: Proxies, Relay, Search, Settings. Navigation preserves drafts.
- Monospace, flat borders, light/dark system preference and narrow-screen layout; no external fonts or frontend dependencies on the router.
- Each proxy has a Rename action inside Connection and details. Saving a name changes only that profile.
- Relay remains separate. Only the activation link is visible in the basic creation form. A new Relay is saved disabled; saving does not activate it or register an account.
- Source IP/interface, path acknowledgement and monitoring interval are under Advanced. URL-only saving works, but automatic source selection is not implemented: first activation still requires a configured, acknowledged outgoing path. An unconfigured enable opens the relevant settings instead of inventing consent.
- Relay proxy details show the configured remote server, not the private loopback bridge. Server country is not a guarantee of public-IP country.

## Exit policy

Policy is configured per proxy under Advanced:

- `off`: geography does not block access. Fresh transport, applied-generation, ownership, credentials and Relay dependency checks remain mandatory.
- `exclusions`: reject excluded observed nodes, observed node countries and public-IP countries. Unknown required geography fails closed.
- `strict`: apply exclusions and require fresh independent geolocation to match the explicitly selected `policy_country`, with an agreeing current `/meta` public-IP country. Unknown or conflicting evidence blocks access. The Relay server selection does not fill or guarantee this target.

A country change is detected by periodic health/geolocation checks, not per packet. Until a new check completes, the previous valid result may remain usable within its freshness window. A failed policy closes the app-owned public gate; changing mode is not a router firewall or routing operation.

Existing profiles without an explicit policy retain compatibility behavior until the user changes it. An unrelated name edit must not silently migrate their policy.

## Service acknowledgement

Thanks to [llimonix](https://github.com/nellimonix) for the external Relay service. warpd uses its own client implementation; no code from the service's project is included.

## Offline verification

Run the DOM suites with Node and jsdom installed outside the repository:

```sh
GO_BINARY=/path/to/go JSDOM_PATH=/path/to/node_modules/jsdom \
  node --test ui_interaction.test.cjs ui_simple_forms.test.cjs \
  ui_policy_modes.test.cjs ui_workspace.test.cjs ui_credits.test.cjs
```

Go runtime acceptance needs the existing loopback-only, owned-ancestor namespace harness and pinned-engine tests. DOM/browser fixtures establish presentation and API interaction behavior, not live Relay activation, registration, egress or router-kernel acceptance.
