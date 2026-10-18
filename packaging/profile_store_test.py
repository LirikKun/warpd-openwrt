"""Real-shell profile-store lifecycle regressions; no live router or credentials."""
import os
from pathlib import Path
import shutil
import subprocess
import unittest

import installer_test as fixtures

HERE = fixtures.HERE


class ProfilePackagingTests(unittest.TestCase):
    base: Path
    root: Path
    bundle: Path

    # Reuse only fixture helpers, not the parent test suite.
    setUp = fixtures.InstallerTests.setUp
    put = fixtures.InstallerTests.put
    checksums = fixtures.InstallerTests.checksums
    run_script = fixtures.InstallerTests.run_script
    install = fixtures.InstallerTests.install
    snapshot = fixtures.InstallerTests.snapshot
    live = fixtures.InstallerTests.live
    no_stat_environment = fixtures.InstallerTests.no_stat_environment

    def run_defaults(self):
        script = self.base / 'defaults.sh'
        script.write_text((HERE.parent / 'warpd.uci-defaults').read_text().replace('/etc', str(self.root / 'etc')))
        tools = self.base / 'defaults-tools'
        self.put(tools / 'uci', '''#!/bin/sh
printf '%s\\n' "$*" >> "$UCI_EVENTS"
[ "$1" != -q ] || shift
case "$1" in
 get) exit 1 ;;
 set) printf '%s\\n' "$2" >> "$UCI_PENDING" ;;
 commit) cp "$UCI_PENDING" "$UCI_CONFIG" ;;
 *) exit 90 ;;
esac
''', 0o755)
        (self.root / 'etc/config').mkdir(parents=True, exist_ok=True)
        return subprocess.run(['sh', str(script)], text=True, capture_output=True,
                              env=dict(os.environ, PATH=str(tools) + ':' + os.environ['PATH'],
                                       UCI_EVENTS=str(self.base / 'uci-events'), UCI_PENDING=str(self.base / 'uci-pending'),
                                       UCI_CONFIG=str(self.root / 'etc/config/warpd')))

    def test_purge_removes_exact_profile_secret_path_and_discloses_scope(self):
        self.install()
        profile = self.root / 'etc/warpd/profiles.json'
        self.put(profile, 'PRIVATE SYNTHETIC RELAY CAPABILITY', 0o600)
        self.put(self.root / 'etc/warpd/profiles.json.user-backup', 'unknown backup', 0o600)
        self.put(self.root / 'etc/warpd/warp-account.json', 'TEST ACCOUNT', 0o600)
        result = self.run_script('uninstall.sh', '--purge')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(profile.exists(), 'purge retained the known secret store')
        for path in ('/etc/config/warpd', '/etc/warpd/warp-account.json', '/etc/warpd/profiles.json'):
            self.assertIn(path, result.stderr)
        self.assertIn('Relay activation credentials', result.stderr)
        self.assertTrue((self.root / 'etc/warpd/profiles.json.user-backup').exists())
        self.assertNotIn('PRIVATE SYNTHETIC', result.stdout + result.stderr)
        again = self.run_script('uninstall.sh', '--purge')
        self.assertEqual(again.returncode, 0, again.stderr)

    def test_failed_first_start_rolls_back_daemon_created_profile_store(self):
        self.put(self.base / 'fail-start', '1')
        result = self.live('sh /opt/bundle/install.sh', start_hook='''
  mkdir -p /etc/warpd
  printf '%s' 'TEST MIGRATED STORE' > /etc/warpd/profiles.json
  chmod 600 /etc/warpd/profiles.json
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Service start failed', result.stderr)
        self.assertFalse((self.root / 'etc/warpd/profiles.json').exists(), 'failed startup left a migration store behind')
        self.assertFalse((self.root / 'etc/config/warpd').exists())
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_external_dependency_refuses_secret_and_service_config_paths(self):
        candidates = ('etc/warpd/profiles.json', 'etc/warpd/warp-account.json', 'etc/config/ssclash',
                      'opt/clash/config.yaml', 'opt/ssclash/configs/custom')
        for index, relative in enumerate(candidates):
            path = self.root / relative
            self.put(path, '#!/bin/sh\n# TEST private config\nexit 0\n', 0o700)
            alias = self.base / ('alias-' + str(index))
            alias.symlink_to(path)
            before = self.snapshot()
            for name in ('mihomo', 'warpscout'):
                for candidate in (path, alias):
                    with self.subTest(name=name, candidate=candidate):
                        result = self.run_script('install.sh', '--' + name, str(candidate))
                        self.assertNotEqual(result.returncode, 0, result.stdout)
                        self.assertEqual(self.snapshot(), before)
        secret = self.root / 'etc/warpd/profiles.json'
        hardlink = self.base / 'hardlinked-secret'
        os.link(secret, hardlink)
        result = self.run_script('install.sh', '--mihomo', str(hardlink))
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_upgrade_preserves_profile_bytes_and_makes_store_private(self):
        profile = self.root / 'etc/warpd/profiles.json'
        contents = b'{ "schema":1, "profiles":{}, "relays":{} }\r\n'
        self.put(profile, contents.decode(), 0o644)
        self.put(self.root / 'etc/config/warpd', "config warpd 'global'\n option enabled '0'\n", 0o600)
        profile.parent.chmod(0o755)
        self.install()
        self.assertEqual(profile.read_bytes(), contents)
        self.assertEqual(profile.stat().st_mode & 0o7777, 0o600)
        self.assertEqual(profile.parent.stat().st_mode & 0o777, 0o700)

    def test_hardlinked_profile_store_is_refused_before_chmod(self):
        profile = self.root / 'etc/warpd/profiles.json'
        shared = self.base / 'shared-original'
        self.put(shared, 'TEST SHARED ORIGINAL', 0o644)
        profile.parent.mkdir(parents=True)
        os.link(shared, profile)
        before = self.snapshot()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(shared.stat().st_mode & 0o7777, 0o644)
        self.assertEqual(shared.read_bytes(), b'TEST SHARED ORIGINAL')

    def test_legacy_upgrade_retains_all_uci_sections_without_creating_store(self):
        legacy = ("# TEST 2.2.0 preserved legacy configuration\nconfig warpd 'global'\n"
                  " option enabled '0'\n option web_language 'ru'\n option exclude_countries 'RU,BY'\n"
                  "config warp 'warp'\n option enabled '0'\n option port '4101'\n option endpoint '192.0.2.1:2408'\n"
                  "config warp 'masque'\n option enabled '1'\n option port '4103'\n"
                  "config warp 'masqueh2'\n option enabled '1'\n option port '4104'\n"
                  "config warp 'm2w'\n option enabled '1'\n option port '4105'\n"
                  " option outer_endpoint '192.0.2.2:443'\n option inner_endpoint '192.0.2.3:2408'\n")
        self.put(self.root / 'etc/config/warpd', legacy, 0o600)
        self.put(self.root / 'etc/warpd/warp-account.json', 'TEST ACCOUNT', 0o600)
        self.put(self.root / 'usr/bin/warpd', 'TEST LEGACY BINARY', 0o755)
        for _ in range(2):
            self.install()
            self.assertEqual((self.root / 'etc/config/warpd').read_bytes(), legacy.encode())
            self.assertEqual((self.root / 'etc/warpd/warp-account.json').read_bytes(), b'TEST ACCOUNT')
            self.assertFalse((self.root / 'etc/warpd/profiles.json').exists())
            result = self.run_defaults()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((self.root / 'etc/config/warpd').read_bytes(), legacy.encode())

    def test_empty_invalid_and_future_stores_are_preserved_across_upgrade_and_uninstall(self):
        profile = self.root / 'etc/warpd/profiles.json'
        for content in (b'', b'{broken', b'{"schema":999,"secret":"TEST ONLY"}',
                        b'{"schema":1,"profiles":{},"relays":{}}\n'):
            with self.subTest(content=content):
                self.put(profile, content.decode(), 0o600)
                self.install()
                self.assertEqual(profile.read_bytes(), content)
                self.assertEqual(profile.stat().st_mode & 0o7777, 0o600)
                result = self.run_script('uninstall.sh')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(profile.read_bytes(), content)
                self.assertEqual(profile.stat().st_mode & 0o7777, 0o600)
                self.assertIn('Relay activation credentials preserved', result.stdout)

    def test_profile_store_symlinks_and_special_files_refuse_install_and_purge_without_writes(self):
        self.install()
        profile = self.root / 'etc/warpd/profiles.json'
        outside = self.base / 'outside-secret'
        self.put(outside, 'TEST OUTSIDE SECRET', 0o640)
        for kind in ('link', 'dangling', 'directory', 'fifo'):
            with self.subTest(kind=kind):
                if kind == 'link': profile.symlink_to(outside)
                elif kind == 'dangling': profile.symlink_to(self.base / 'missing')
                elif kind == 'directory': profile.mkdir()
                else: os.mkfifo(profile)
                # Avoid opening the FIFO while collecting the snapshot.
                profile_mode = profile.lstat().st_mode
                binaries = (self.root / 'usr/bin/warpd').read_bytes()
                for script, args in [('install.sh', ()), ('uninstall.sh', ('--purge',))]:
                    result = self.run_script(script, *args)
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertEqual(profile.lstat().st_mode, profile_mode)
                    self.assertEqual((self.root / 'usr/bin/warpd').read_bytes(), binaries)
                    self.assertEqual(outside.read_bytes(), b'TEST OUTSIDE SECRET')
                    self.assertEqual(outside.stat().st_mode & 0o7777, 0o640)
                    self.assertFalse(list(self.root.rglob('.warpd-stage.*')))
                if kind == 'directory': profile.rmdir()
                else: profile.unlink()

    def test_profile_parent_symlink_is_refused_for_install_and_purge(self):
        self.install()
        private = self.root / 'etc/warpd'
        outside = self.base / 'outside-private'
        private.rename(outside)
        private.symlink_to(outside, target_is_directory=True)
        self.put(outside / 'profiles.json', 'TEST OUTSIDE STORE', 0o600)
        before = self.snapshot()
        for script, args in [('install.sh', ()), ('uninstall.sh', ('--purge',))]:
            result = self.run_script(script, *args)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertEqual(self.snapshot(), before)
            self.assertEqual((outside / 'profiles.json').read_bytes(), b'TEST OUTSIDE STORE')

    def test_profile_backup_is_included_in_peak_disk_budget(self):
        self.put(self.root / 'etc/warpd/profiles.json', 'x' * (3 * 1024 * 1024), 0o600)
        before = self.snapshot()
        tools = self.base / 'space-tools'
        self.put(tools / 'df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 4096 2048 2048 50%% /\\n"\n', 0o755)
        result = self.run_script(env=dict(os.environ, PATH=str(tools) + ':' + os.environ['PATH']))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('space', result.stderr)
        self.assertEqual(self.snapshot(), before)

    def test_profile_permission_failure_restores_all_bytes_and_modes(self):
        self.put(self.root / 'etc/warpd/profiles.json', 'TEST PRIVATE PROFILES', 0o644)
        self.put(self.root / 'etc/warpd/warp-account.json', 'TEST ACCOUNT', 0o640)
        self.put(self.root / 'etc/config/warpd', '# TEST ORIGINAL UCI\n', 0o640)
        self.put(self.root / 'usr/bin/warpd', 'TEST ORIGINAL BINARY', 0o751)
        before = self.snapshot()
        tools = self.base / 'chmod-tools'
        self.put(tools / 'chmod', '''#!/bin/sh
REAL_CHMOD "$@" || exit
if [ "$1" = 600 ] && [ "$2" = "$PROFILE" ] && [ ! -e "$FAILED" ]; then
 : > "$FAILED"
 exit 71
fi
'''.replace('REAL_CHMOD', shutil.which('chmod')), 0o755)
        result = self.run_script(env=dict(os.environ, PATH=str(tools) + ':' + os.environ['PATH'],
                                         PROFILE=str(self.root / 'etc/warpd/profiles.json'), FAILED=str(self.base / 'failed')))
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertTrue((self.base / 'failed').exists(), 'failure did not reach profile chmod')
        self.assertEqual(self.snapshot(), before)

    def test_failed_upgrade_start_restores_existing_profile_bytes_and_mode(self):
        self.put(self.base / 'fail-start', '2')
        self.put(self.root / 'etc/config/warpd', "config warpd 'global'\n option enabled '1'\n", 0o600)
        self.put(self.root / 'etc/warpd/profiles.json', 'TEST ORIGINAL PROFILES\r\n', 0o600)
        self.put(self.root / 'etc/warpd/unknown', 'TEST UNKNOWN', 0o600)
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
mkdir -p /etc/init.d
cp /opt/bundle/files/warpd.init /etc/init.d/warpd
/etc/init.d/warpd start
sh /opt/bundle/install.sh
status=$?
[ "$status" != 0 ] && /etc/init.d/warpd running
''', start_hook='''
  if [ "$n" = 2 ]; then
   printf '%s' 'TEST STARTUP CHANGED STORE' > /etc/warpd/profiles.json
   chmod 644 /etc/warpd/profiles.json
  fi
''')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Service start failed', result.stderr)
        profile = self.root / 'etc/warpd/profiles.json'
        self.assertEqual(profile.read_bytes(), b'TEST ORIGINAL PROFILES\r\n')
        self.assertEqual(profile.stat().st_mode & 0o7777, 0o600)
        self.assertEqual((profile.parent / 'unknown').read_bytes(), b'TEST UNKNOWN')
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_busybox_private_profile_lifecycle_without_stat(self):
        env, _, busybox = self.no_stat_environment(busybox=True)
        shell = [busybox, 'ash']
        self.install(env=env, shell=shell)
        profile = self.root / 'etc/warpd/profiles.json'
        self.put(profile, '{"schema":1,"profiles":{},"relays":{}}\n', 0o600)
        self.put(self.root / 'etc/warpd/unknown', 'TEST UNKNOWN')
        original = profile.read_bytes()
        self.install('--language', 'ru', env=env, shell=shell)
        self.assertEqual(profile.read_bytes(), original)
        self.assertEqual(profile.stat().st_mode & 0o7777, 0o600)
        result = self.run_script('uninstall.sh', env=env, shell=shell)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(profile.read_bytes(), original)
        result = self.run_script('uninstall.sh', '--purge', env=env, shell=shell)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(profile.exists())
        self.assertTrue((profile.parent / 'unknown').exists())

    def test_defaults_preserve_existing_empty_uci_without_uci_calls(self):
        config = self.root / 'etc/config/warpd'
        self.put(config, '', 0o600)
        before = config.read_bytes()
        for replay in range(2):
            result = self.run_defaults()
            self.assertEqual(result.returncode, 0, result.stderr)
            with self.subTest(replay=replay, invariant='config bytes'):
                self.assertEqual(config.read_bytes(), before)
            with self.subTest(replay=replay, invariant='no UCI calls'):
                self.assertFalse((self.base / 'uci-events').exists())

    def test_defaults_with_existing_store_do_not_create_uci_or_profiles(self):
        self.put(self.root / 'etc/warpd/profiles.json', '{"schema":1,"profiles":{}}', 0o600)
        result = self.run_defaults()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.root / 'etc/config/warpd').exists())
        self.assertFalse((self.base / 'uci-events').exists())

    def test_defaults_refuse_symlinked_config_ancestors(self):
        outside = self.base / 'outside-config'
        outside.mkdir()
        (self.root / 'etc').mkdir()
        (self.root / 'etc/config').symlink_to(outside, target_is_directory=True)
        result = self.run_defaults()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((outside / 'warpd').exists())
        self.assertFalse((self.base / 'uci-events').exists())

    def test_defaults_never_recreate_deleted_profiles_on_replay(self):
        first = self.run_defaults()
        self.assertEqual(first.returncode, 0, first.stderr)
        events = (self.base / 'uci-events').read_bytes()
        self.assertIn(b'warpd.global.profile_schema=1', events)
        self.assertNotIn(b'warpd.masque', events)
        config = self.root / 'etc/config/warpd'
        config.write_text("config warpd 'global'\n option profile_schema '1'\n")
        before = self.snapshot()
        result = self.run_defaults()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual((self.base / 'uci-events').read_bytes(), events)


if __name__ == '__main__':
    unittest.main()
