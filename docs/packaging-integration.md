# Packaging integration contract

Packaging uses `warpd.global.profile_schema='1'` on **fresh defaults only**. The fresh UCI seed contains global + WARP, no MASQUE/H2/chain sections. Core must distinguish this marker when importing UCI: schema 1 denotes the fresh WARP-only seed; unmarked legacy 2.2.0 configuration imports all legacy sections/settings unchanged. An existing dynamic store is authoritative, including an intentionally empty profile map. Existing UCI is never marked/replayed by an upgrade.

Core `InitProfiles(/etc/warpd/profiles.json)` runs once at startup. The installer never creates an empty store, because that would suppress migration. Existing store bytes are preserved, including empty, invalid, or future-schema files; core reports errors rather than replacing them. The directory is private 0700, store 0600.

No separate migration backup file is planned: legacy `/etc/config/warpd` remains untouched and acts as the original migration input. If core introduces a backup, agree its ONE exact pathname before integration; packaging must add that exact path to preservation, rollback and explicit purge. Never glob secret files.

Ordinary uninstall preserves UCI, WARP account and profile store. `--purge` deletes exactly `/etc/config/warpd`, `/etc/warpd/warp-account.json`, `/etc/warpd/profiles.json`; unknown files stay. The warning explicitly includes Relay activation credentials in the profile store.

Installer rollback must restore the prior profile file (or prior absence) after a failing daemon startup, including daemon-created migration output. Unknown files are never rolled back or removed. No live router operations are part of packaging tests.

## Integrated bootstrap safeguards

The root `install.sh` includes one rollback copy of `/etc/warpd/profiles.json` in its `OLD` peak-space calculation. Early external-dependency validation matches the inner installer's refusal of `/etc/*`, configuration-shaped paths (`config`, `config.*`, `config/`, `configs/`, and `.json/.yaml/.yml/.uci/.conf/.config/.key`) and hardlink aliases of known private files. It checks both supplied and `readlink -f` paths. Inner validation remains authoritative. Offline namespace regressions exercise rejection before downloads and insufficient space for the profile rollback copy; the full packaging suite also runs with BusyBox ash/applets.

The shell installer refuses symlink/special profile targets, symlink ancestors, and multiply linked stores before chmod; dependencies are never chmodded or copied over their shared originals. Runtime must independently enforce private-store path safety: shell installation checks do not protect a later daemon read/write from an unsafe path introduced afterwards. In particular `os.ReadFile` follows links and `os.MkdirAll(...,0700)` does not tighten an existing directory's permissions; core tests should cover these separately.
