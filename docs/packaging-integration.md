# Packaging integration contract

Packaging uses `warpd.global.profile_schema='1'` on **fresh defaults only**. The fresh UCI seed contains global + WARP, no MASQUE/H2/chain sections. Core must distinguish this marker when importing UCI: schema 1 denotes the fresh WARP-only seed; unmarked legacy 2.2.0 configuration imports all legacy sections/settings unchanged. An existing dynamic store is authoritative, including an intentionally empty profile map. Existing UCI is never marked/replayed by an upgrade.

Core `InitProfiles(/etc/warpd/profiles.json)` runs once at startup. The installer never creates an empty store, because that would suppress migration. Existing store bytes are preserved, including empty, invalid, or future-schema files; core reports errors rather than replacing them. The directory is private 0700, store 0600.

No separate migration backup file is planned: legacy `/etc/config/warpd` remains untouched and acts as the original migration input. If core introduces a backup, agree its ONE exact pathname before integration; packaging must add that exact path to preservation, rollback and explicit purge. Never glob secret files.

Ordinary uninstall preserves UCI, WARP account and profile store. `--purge` deletes exactly `/etc/config/warpd`, `/etc/warpd/warp-account.json`, `/etc/warpd/profiles.json`; unknown files stay. The warning explicitly includes Relay activation credentials in the profile store.

Installer rollback must restore the prior profile file (or prior absence) after a failing daemon startup, including daemon-created migration output. Unknown files are never rolled back or removed. No live router operations are part of packaging tests.

## Integrated bootstrap safeguards

The public `install-warpd.sh` includes one rollback copy of `/etc/warpd/profiles.json` in its `OLD` peak-space calculation. Early external-dependency validation matches the inner installer's refusal of `/etc/*`, configuration-shaped paths (`config`, `config.*`, `config/`, `configs/`, and `.json/.yaml/.yml/.uci/.conf/.config/.key`) and hardlink aliases of known private files. It checks both supplied and `readlink -f` paths. Inner validation remains authoritative. Offline namespace regressions exercise rejection before downloads and insufficient space for the profile rollback copy; the full packaging suite also runs with BusyBox ash/applets.

The shell installer refuses symlink/special profile targets, symlink ancestors, and multiply linked stores before chmod; dependencies are never chmodded or copied over their shared originals. Runtime must independently enforce private-store path safety: shell installation checks do not protect a later daemon read/write from an unsafe path introduced afterwards. In particular `os.ReadFile` follows links and `os.MkdirAll(...,0700)` does not tighten an existing directory's permissions; core tests should cover these separately.

## Archive-backed installation (new bundle format support)

Newly built installers contain the exact capability comment `# warpd-archive-install-v1`. The bootstrap materializes bounded control files from a checksum-verified archive, then invokes the bundled installer with `--archive /absolute/path/to/bundle.tar.gz`. This is not `tar | sh`: the script is a regular bounded file. The inner installer independently validates the complete member inventory and every included SHA-256 row, including documentation and dependencies explicitly reused from external paths. It checks tar's producer exit separately from the hash consumer.

The compressed archive remains available until installation finishes. Required payloads stream directly into the existing private, destination-local `.warpd-stage.*` journals, with extraction status, exact size and staged digest verification. They are not fully unpacked into `/tmp`. Existing rollback copies, service-stop checks, post-stop profile snapshots and startup verification are retained. The inner installer never deletes a caller-supplied archive. Directory-mode installation remains supported.

`/tmp` storage, available process memory and destination transaction space are separate constraints. If `/tmp` shares the destination filesystem, archive/control bytes and transaction allocations coexist and must be budgeted together. A downloaded archive is already reflected in `df` and must not be charged twice against remaining free space. RAM checks also include additional transaction allocations on RAM-backed destination mounts, including nested tmpfs/ramfs mounts. A decompressor reserve is not an RSS guarantee. Package-manager changes remain an explicit, non-rollbackable operation; they are not covered by the portable file transaction.

An explicit `--language en|ru` edit uses a strict two-pass streaming parser: validate the staged configuration, then rewrite only the selected option. It does not retain every record in memory. Records larger than 65,536 bytes are safely refused before service/file commit **only when a language edit is requested**. An upgrade without a language override preserves raw configuration bytes without this record-size limit. The editor retains section/syntax checks, comments, CRLF, missing-final-newline handling and the separate output-copy storage reserve.

The initial compressed-download ceilings are 40 MiB for `full`, 12 MiB for `no-mihomo`, 36 MiB for `no-warpscout`, and 8 MiB for `core`. The selected ceiling is used consistently in the downloader and initial preflight, with an additional 1 MiB control-storage reserve and 9 MiB RAM planning reserve. Thus the corresponding initial free-`/tmp` gates are 41/13/37/9 MiB, not the observed archive sizes or a guarantee that any configuration or firmware fits. Verified inventory, destination capacity and post-download RAM checks still apply.

### Fixed bundle variants

The builder accepts `--variant full|no-mihomo|no-warpscout|core` (default `full`). Full preserves the historical archive naming and 14-file/four-directory layout, with no `VARIANT` member. The other variants append `-no-mihomo`, `-no-warpscout` or `-core` to the archive/root name and include that exact enum in `VARIANT` with a trailing newline. Their manifests cover precisely the included files; omitted dependencies are neither downloaded nor extracted by the builder.

- `no-mihomo` requires the operator's explicit `--mihomo /absolute/path`.
- `no-warpscout` requires explicit `--warpscout /absolute/path`.
- `core` requires both explicit reuse arguments.

No dependency is auto-discovered, executed as a probe, chmodded or overwritten at its external location. The private warpd dependency pathname becomes a symlink to the explicitly supplied executable. A full archive still verifies an included dependency even when it will be reused instead of installed.

### Historical release compatibility and publication

The release default is `3.1.2`, with archive-backed installation and all four variants for arm64 and amd64. The published `3.1.1` bundle has no archive-mode marker and therefore takes the legacy full-extraction path, as do older bundles. Updating the public bootstrap alone does not retroactively optimize a historical bundled installer. Historical fallback retains its full extraction-space checks.

For numeric versions `>=3.1.2`, explicit reuse selects the corresponding slim asset; otherwise the full asset is requested. A supporting release must publish all four variants for each supported architecture, each with its SHA-256 sidecar. Never point the default at such a release before those assets and the new bundled installer have been built, reviewed and published. Version/tag/default changes and live router acceptance are separate authorized steps, not part of offline packaging tests.

Regression coverage executes the real scripts in isolated namespaces with BusyBox/no-`stat`, pipe/PTY prompts, corrupted and oversized archives, extractor failures after valid output, rollback faults, shared-filesystem boundaries, historical bundles and configuration/profile preservation. Real-payload acceptance also distinguishes verified cached daemon/dependency binaries from newly compiled release binaries; packaging smoke artifacts are not release authorization.
