"""Execute production POSIX scripts against isolated offline roots.
Payloads below are executable TEST fixtures, never distributable binaries.
Run: PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging -p installer_test.py -v
Optional: WARPD_TEST_BUSYBOX=/path/to/busybox exercises ash with BusyBox-only PATH.

Incremental TDD ledger: each implementation slice below was run RED against
its then-current real shell script, then GREEN in a full-suite run:
- Fresh offline layout: missing installer -> executable private files/modes.
- Integrity: tampered payload installed successfully -> rejected without writes.
- Input/platform/ARCH guards: relative root and bad ARCH accepted -> refused.
- Default config: missing global enabled value -> UCI defaults mirrored exactly.
- Path/manifest guards: symlink ancestor escaped root -> safe rejection.
- External dependencies: regular copies instead of private links -> safe links.
- Transaction: injected cp/mv failure left changed files -> exact rollback.
- Upgrade permissions: new usr/share was 0700 -> normal directories 0755.
- Preflight: fake low-space df still installed -> no changes on shortage.
- Uninstall keep: stub failed -> allowlisted removal and sentinel preservation.
- Purge: unknown flag -> only config/known account removed with warning.
- New live-mode fixture: no enable/start events -> verified service startup.
- Upgrade service: enabled autostart instead of stop/start -> state preserved.
- Shutdown: dependency rename preceded old child exit -> bounded child wait.
- Failed upgrade start: old runtime not restarted -> files/runtime restored.
- Failed fresh start: new boot link remained -> original boot links restored.
- Live uninstall: no stop/disable events -> only owned service stopped/disabled.
- Ownership conflicts: unowned private file overwritten -> preflight refusal.
- Failed rollback rename: original backup deleted -> recovery backup retained.
- Self-link: late mv error rather than preflight -> installer-owned paths refused.
- Slash aliases: --root // installed into / -> root aliases rejected.
- Journal separator: pipe in root corrupted journal -> rejected before staging.
- Missing UCI: install succeeded -> prerequisite failure before writes.
- Thread children: nonleader-thread child missed -> every task's children waited.
- Unmanaged running daemon: overwritten without init -> explicit safe refusal.
- Space accounting: preserved config copied twice under-budget -> both counted.
- Foreign boot link: uninstall deleted it -> refusal before service operations.
- Missing init during uninstall: running files deleted -> retained with error.
- External reuse space: unused bundle copies budgeted -> symlink-only cost.
- Minimal OpenWrt PATH (host/BusyBox tools): missing stat rejected all installs
  -> fresh install, config/account-preserving upgrade, special-bit mode rollback.
Other tests are regression coverage of these slices, including BusyBox ash,
asynchronous start failure, disabled/stopped state, and host-tool tripwires.
"""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='warpd-installer-', dir=os.environ.get('TMPDIR'))
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.bundle = self.base / 'bundle'
        self.root = self.base / 'root'
        self.bundle.mkdir()
        self.root.mkdir()
        for name in ('install.sh', 'uninstall.sh'):
            if (HERE / name).exists():
                shutil.copy2(HERE / name, self.bundle / name)
        for name in ('warpd', 'mihomo', 'warpscout'):
            self.put(self.bundle / 'bin' / name, '#!/bin/sh\n# TEST fixture only\nexit 0\n', 0o755)
        self.put(self.bundle / 'files/warpd.init', '#!/bin/sh\nPROG=/usr/bin/warpd\nexit 0\n', 0o755)
        if (HERE / 'warpd.config').exists():
            shutil.copy2(HERE / 'warpd.config', self.bundle / 'files/warpd.config')
        else:
            self.put(self.bundle / 'files/warpd.config', "config warpd 'global'\n")
        self.put(self.bundle / 'VERSION', '2.1.1\n')
        self.put(self.bundle / 'ARCH', 'arm64\n')
        for name in ('README.md', 'THIRD_PARTY.md', 'licenses/warpscout-MIT.txt', 'licenses/mihomo-GPL-3.0.txt'):
            self.put(self.bundle / name, 'TEST fixture\n')
        self.checksums()

    def put(self, path, text, mode=0o644):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        path.chmod(mode)

    def checksums(self):
        self.put(self.bundle / 'SHA256SUMS', ''.join(
            hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.relative_to(self.bundle).as_posix() + '\n'
            for p in sorted(self.bundle.rglob('*')) if p.is_file() and p.name != 'SHA256SUMS'))

    def run_script(self, script='install.sh', *args, offline=True, env=None, shell='sh'):
        cmd = (list(shell) if isinstance(shell, (list, tuple)) else [shell]) + [str(self.bundle / script)]
        if offline:
            cmd += ['--root', str(self.root)]
        return subprocess.run(cmd + list(args), cwd=self.base, text=True, capture_output=True, env=env)

    def install(self, *args, **kwargs):
        result = self.run_script('install.sh', *args, **kwargs)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def test_fresh_offline_installs_private_files_without_starting_service(self):
        result = self.install()
        for name in ('usr/bin/warpd', 'usr/libexec/warpd/mihomo', 'usr/libexec/warpd/warpscout', 'etc/init.d/warpd', 'usr/sbin/warpd-uninstall'):
            self.assertTrue((self.root / name).is_file(), name)
            self.assertTrue(os.access(self.root / name, os.X_OK), name)
        self.assertIn('not started', result.stdout)
        self.assertEqual((self.root / 'usr/share/warpd/VERSION').read_text(), '2.1.1\n')
        self.assertEqual((self.root / 'etc/config/warpd').stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.root / 'etc/warpd').stat().st_mode & 0o777, 0o700)

    def snapshot(self):
        return {str(p.relative_to(self.root)): (('link', os.readlink(p)) if p.is_symlink() else ('dir', p.stat().st_mode & 0o777) if p.is_dir() else ('file', p.read_bytes(), p.stat().st_mode & 0o777)) for p in self.root.rglob('*')}

    def test_checksum_validation_precedes_all_mutations(self):
        self.put(self.bundle / 'bin/mihomo', 'tampered')
        before = self.snapshot()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.snapshot(), before)

    def test_invalid_invocations_and_metadata_leave_root_untouched(self):
        cases = [('--wat',), ('--root',), ('--root', 'relative'), ('--mihomo', 'relative'), ('--warpscout', '/does-not-exist')]
        for args in cases:
            with self.subTest(args=args):
                result = self.run_script('install.sh', *args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.snapshot(), {})
        for arch in ('bogus\n', 'arm64\n../evil\n'):
            self.put(self.bundle / 'ARCH', arch)
            self.checksums()
            result = self.run_script()
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(self.snapshot(), {})
        self.put(self.bundle / 'ARCH', 'arm64\n')
        self.checksums()
        result = self.run_script(offline=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('OpenWrt', result.stderr)

    def test_fresh_config_matches_uci_defaults_and_only_warp_is_enabled(self):
        self.install()
        config = (self.root / 'etc/config/warpd').read_text()
        import re
        sections = {}
        current = None
        for line in config.splitlines():
            match = re.match(r"config (\w+) '(\w+)'", line)
            if match:
                current = sections.setdefault(match[2], {})
            match = re.match(r"\s*option (\w+) '([^']*)'", line)
            if match:
                self.assertIsNotNone(current)
                assert current is not None
                current[match[1]] = match[2]
        self.assertEqual(sections.get('global', {}).get('enabled'), '1')
        self.assertEqual(sections['global']['web_port'], '9095')
        self.assertEqual(sections['global']['web_listen'], '0.0.0.0')
        self.assertEqual(sections['global']['web_secret'], '')
        self.assertEqual(sections['global']['check_period'], '300')
        self.assertEqual(sections['global']['autoscan_on_start'], '1')
        for name, port in [('warp', '4001'), ('masque', '4003'), ('masqueh2', '4004'), ('m2w', '4005')]:
            self.assertEqual(sections[name]['enabled'], '1' if name == 'warp' else '0')
            self.assertEqual(sections[name]['port'], port)
            self.assertEqual(sections[name]['listen'], '0.0.0.0')
        defaults = (HERE.parent / 'warpd.uci-defaults').read_text()
        expected = re.findall(r"uci set warpd\.(\w+)\.(\w+)='([^']*)'", defaults)
        for section, field, value in expected:
            self.assertEqual(sections[section][field], value, f'{section}.{field}')

    def test_symlink_ancestors_and_incomplete_manifests_are_rejected(self):
        outside = self.base / 'outside'
        outside.mkdir()
        (self.root / 'usr').symlink_to(outside, target_is_directory=True)
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(outside.iterdir()), [])
        (self.root / 'usr').unlink()
        (self.bundle / 'SHA256SUMS').write_text('')
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.snapshot(), {})
        self.checksums()
        manifest = self.bundle / 'SHA256SUMS'
        original = manifest.read_text()
        for changed in (original.replace('  bin/mihomo', '  ../outside'), original + original.splitlines()[0] + '\n', '\n'.join(line for line in original.splitlines() if not line.endswith('bin/mihomo')) + '\n'):
            manifest.write_text(changed)
            result = self.run_script()
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(self.snapshot(), {})

    def test_external_dependencies_are_private_links_and_shared_files_untouched(self):
        mihomo = self.base / 'external-mihomo'
        scout = self.base / 'external-scout'
        self.put(mihomo, '#!/bin/sh\nexit 0\n', 0o755)
        self.put(scout, '#!/bin/sh\nexit 0\n', 0o755)
        self.put(self.root / 'usr/bin/warpscout', 'shared scout sentinel')
        self.put(self.root / 'opt/clash/bin/clash', 'shared mihomo sentinel')
        self.install('--mihomo', str(mihomo), '--warpscout', str(scout))
        self.assertTrue((self.root / 'usr/libexec/warpd/mihomo').is_symlink())
        self.assertEqual(os.readlink(self.root / 'usr/libexec/warpd/mihomo'), str(mihomo))
        self.assertEqual(os.readlink(self.root / 'usr/libexec/warpd/warpscout'), str(scout))
        self.assertEqual((self.root / 'usr/bin/warpscout').read_text(), 'shared scout sentinel')
        self.assertEqual((self.root / 'opt/clash/bin/clash').read_text(), 'shared mihomo sentinel')

    def test_failed_copy_or_atomic_replace_rolls_back_exactly(self):
        self.put(self.root / 'usr/bin/warpd', 'old manual binary', 0o755)
        self.put(self.root / 'etc/config/warpd', "# preserve bytes\nconfig warpd 'global'\n option enabled '0'\n", 0o600)
        self.put(self.root / 'etc/warpd/warp-account.json', 'PRIVATE TEST ACCOUNT', 0o600)
        before = self.snapshot()
        wrappers = self.base / 'wrappers'
        wrappers.mkdir()
        for command in ('cp', 'mv'):
            with self.subTest(command=command):
                marker = self.base / 'counter'
                marker.unlink(missing_ok=True)
                wrapper = wrappers / command
                self.put(wrapper, f'#!/bin/sh\nn=0\n[ ! -f "{marker}" ] || n=$(cat "{marker}")\nn=$((n+1))\nprintf "%s" "$n" > "{marker}"\n[ "$n" != 4 ] || exit 71\nexec {shutil.which(command)} "$@"\n', 0o755)
                env = dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH'])
                result = self.run_script(env=env)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertEqual(self.snapshot(), before)
                wrapper.unlink()

    def test_upgrade_preserves_disabled_config_account_and_boot_links(self):
        config = "# custom disabled config, do not replay defaults\nconfig warpd 'global'\n option enabled '0'\n"
        self.put(self.root / 'etc/config/warpd', config, 0o600)
        self.put(self.root / 'etc/warpd/warp-account.json', 'PRIVATE TEST ACCOUNT', 0o600)
        self.put(self.root / 'usr/bin/warpd', 'OLD MANUAL', 0o755)
        (self.root / 'etc/rc.d').mkdir()
        (self.root / 'etc/rc.d/S99warpd').symlink_to('../init.d/warpd')
        self.install()
        self.assertEqual((self.root / 'etc/config/warpd').read_text(), config)
        self.assertEqual((self.root / 'etc/warpd/warp-account.json').read_text(), 'PRIVATE TEST ACCOUNT')
        self.assertEqual(os.readlink(self.root / 'etc/rc.d/S99warpd'), '../init.d/warpd')
        self.assertEqual((self.root / 'usr').stat().st_mode & 0o777, 0o755)
        self.assertEqual((self.root / 'usr/share').stat().st_mode & 0o777, 0o755)
        self.install()
        self.assertEqual((self.root / 'etc/config/warpd').read_text(), config)

    def test_low_disk_and_non_owned_targets_fail_before_staging(self):
        wrapper = self.base / 'wrappers'
        wrapper.mkdir()
        self.put(wrapper / 'df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 100 99 1 99%% /\\n"\n', 0o755)
        env = dict(os.environ, PATH=str(wrapper) + ':' + os.environ['PATH'])
        result = self.run_script(env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('space', result.stderr)
        self.assertEqual(self.snapshot(), {})
        self.put(self.root / 'etc/config/other', 'sentinel')
        (self.root / 'etc/config/warpd').symlink_to('other')
        before = self.snapshot()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.snapshot(), before)

    def test_uninstall_keeps_config_accounts_shared_targets_and_unknown_sentinels(self):
        external = self.base / 'external'
        self.put(external, '#!/bin/sh\nexit 0\n', 0o755)
        self.install('--mihomo', str(external))
        self.put(self.root / 'etc/warpd/warp-account.json', 'PRIVATE TEST ACCOUNT', 0o600)
        self.put(self.root / 'etc/warpd/sentinel', 'unknown')
        self.put(self.root / 'usr/libexec/warpd/sentinel', 'unknown runtime')
        self.put(self.root / 'usr/share/warpd/sentinel', 'unknown metadata')
        self.put(self.root / 'usr/bin/warpscout', 'shared')
        self.put(self.root / 'opt/clash/bin/clash', 'shared')
        config = (self.root / 'etc/config/warpd').read_bytes()
        result = self.run_script('uninstall.sh')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        self.assertFalse((self.root / 'usr/libexec/warpd/mihomo').is_symlink())
        self.assertTrue(external.exists())
        self.assertEqual((self.root / 'etc/config/warpd').read_bytes(), config)
        self.assertTrue((self.root / 'etc/warpd/warp-account.json').exists())
        for sentinel in ('etc/warpd/sentinel', 'usr/libexec/warpd/sentinel', 'usr/share/warpd/sentinel', 'usr/bin/warpscout', 'opt/clash/bin/clash'):
            self.assertTrue((self.root / sentinel).exists(), sentinel)
        result = self.run_script('uninstall.sh')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_purge_removes_only_config_and_known_account_with_warning(self):
        self.install()
        self.put(self.root / 'etc/warpd/warp-account.json', 'PRIVATE TEST ACCOUNT', 0o600)
        self.put(self.root / 'etc/warpd/sentinel', 'unknown')
        (self.root / 'etc/rc.d').mkdir()
        (self.root / 'etc/rc.d/S99warpd').symlink_to('../init.d/warpd')
        (self.root / 'etc/rc.d/K10warpd').symlink_to('../init.d/warpd')
        (self.root / 'etc/rc.d/S01other').symlink_to('../init.d/other')
        # Exercise the actual installed helper, independent of bundle location.
        result = subprocess.run(['sh', str(self.root / 'usr/sbin/warpd-uninstall'), '--root', str(self.root), '--purge'], cwd=self.base, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('account', result.stderr.lower())
        self.assertFalse((self.root / 'etc/config/warpd').exists())
        self.assertFalse((self.root / 'etc/warpd/warp-account.json').exists())
        self.assertTrue((self.root / 'etc/warpd/sentinel').exists())
        self.assertFalse((self.root / 'etc/rc.d/S99warpd').is_symlink())
        self.assertFalse((self.root / 'etc/rc.d/K10warpd').is_symlink())
        self.assertTrue((self.root / 'etc/rc.d/S01other').is_symlink())
        result = self.run_script('uninstall.sh', '--purge')
        self.assertEqual(result.returncode, 0, result.stderr)

    def live(self, command, daemon=None):
        """Real shell in an isolated user/PID/network/mount namespace; no host init."""
        if not shutil.which('bwrap'):
            self.skipTest('bubblewrap unavailable for isolated live-mode fixture')
        for directory in ('etc/rc.d', 'usr/bin', 'usr/sbin', 'usr/libexec', 'usr/share/warpd'):
            (self.root / directory).mkdir(parents=True, exist_ok=True)
        shutil.copy2('/bin/dash', self.root / 'usr/bin/sh')
        self.put(self.root / 'etc/openwrt_release', "DISTRIB_ID='OpenWrt'\n")
        tools = self.base / 'live-tools'
        tools.mkdir(exist_ok=True)
        shutil.copy2(Path('/usr/bin/awk').resolve(), tools / 'awk')
        self.put(tools / 'uci', '#!/bin/sh\n# TEST UCI read-only adapter\ngrep -m1 "option enabled" /etc/config/warpd | tr -cd 01\n', 0o755)
        init = '''#!/bin/sh
# TEST init fixture: manages a real shell executable in an isolated PID namespace.
PROG=/usr/bin/warpd
case "$1" in
 start)
  echo start >> /opt/service-events
  n=0; [ ! -f /opt/start-count ] || n=$(cat /opt/start-count)
  n=$((n+1)); echo "$n" > /opt/start-count
  [ ! -f /opt/fail-start ] || [ "$n" != "$(cat /opt/fail-start)" ] || exit 1
  [ ! -f /opt/no-daemon ] || exit 0
  "$PROG" -c 'while :; do sleep 1000; done' >/dev/null 2>&1 &
  echo "$!" > /opt/service-pid ;;
 stop)
  echo stop >> /opt/service-events
  if [ -f /opt/service-pid ]; then
   pid=$(cat /opt/service-pid)
   kids=$(cat /proc/$pid/task/[0-9]*/children 2>/dev/null)
   if [ -f /opt/slow-stop ]; then
    (sleep 2; kill $kids 2>/dev/null || :) >/dev/null 2>&1 &
   else
    [ -z "$kids" ] || kill $kids 2>/dev/null || :
   fi
   kill "$pid" 2>/dev/null || :
  fi
  rm -f /opt/service-pid ;;
 running) [ -f /opt/service-pid ] && kill -0 "$(cat /opt/service-pid)" 2>/dev/null ;;
 enabled) [ -L /etc/rc.d/S99warpd ] ;;
 enable) echo enable >> /opt/service-events; ln -sf ../init.d/warpd /etc/rc.d/S99warpd ;;
 disable) echo disable >> /opt/service-events; rm -f /etc/rc.d/S99warpd ;;
 *) exit 1 ;;
esac
'''
        self.put(self.bundle / 'files/warpd.init', init, 0o755)
        # Host dash is a real executable TEST payload, not a production daemon.
        shutil.copy2(daemon or '/bin/dash', self.bundle / 'bin/warpd')
        self.put(self.bundle / 'ARCH', 'amd64\n' if os.uname().machine == 'x86_64' else 'arm64\n')
        self.checksums()
        args = ['bwrap', '--unshare-user', '--uid', '0', '--unshare-pid', '--unshare-net', '--die-with-parent', '--ro-bind', '/', '/', '--ro-bind', '/usr/bin', '/mnt', '--bind', str(self.base), '/opt', '--bind', str(tools), '/media', '--proc', '/proc', '--dev', '/dev']
        for directory in ('etc', 'usr/bin', 'usr/sbin', 'usr/libexec', 'usr/share'):
            args += ['--bind', str(self.root / directory), '/' + directory]
        args += ['--setenv', 'PATH', '/media:/mnt', '--chdir', '/opt', '/bin/sh', '-c', command]
        result = subprocess.run(args, text=True, capture_output=True, timeout=45)
        if result.returncode and ('Operation not permitted' in result.stderr or 'No permissions to' in result.stderr):
            self.skipTest('bubblewrap user namespaces unavailable: ' + result.stderr)
        return result

    def test_live_new_install_enables_and_verifies_service(self):
        result = self.live('sh /opt/bundle/install.sh')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        events = self.base / 'service-events'
        self.assertTrue(events.exists(), result.stdout)
        self.assertEqual(events.read_text().splitlines(), ['enable', 'start'])
        self.assertNotIn('not started', result.stdout)

    def test_live_upgrade_restarts_running_service_without_enabling_autostart(self):
        self.put(self.root / 'etc/config/warpd', "config warpd 'global'\n option enabled '1'\n", 0o600)
        result = self.live('cp /opt/bundle/bin/warpd /usr/bin/warpd; mkdir -p /etc/init.d; cp /opt/bundle/files/warpd.init /etc/init.d/warpd; /etc/init.d/warpd start; : > /opt/service-events; sh /opt/bundle/install.sh')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'service-events').read_text().splitlines(), ['stop', 'start'])
        self.assertFalse((self.root / 'etc/rc.d/S99warpd').is_symlink())

    def test_upgrade_waits_for_old_children_before_replacing_dependencies(self):
        self.put(self.base / 'slow-stop', '1')
        self.put(self.base / 'live-tools/mv', '''#!/bin/sh
case "$*" in
 */usr/libexec/warpd/mihomo*)
  pid=$(cat /opt/old-child)
  if kill -0 "$pid" 2>/dev/null && ! grep -q '^State:.*Z' "/proc/$pid/status"; then
   echo premature > /opt/premature
  fi ;;
esac
exec /mnt/mv "$@"
''', 0o755)
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
mkdir -p /etc/init.d
cp /opt/bundle/files/warpd.init /etc/init.d/warpd
/etc/init.d/warpd start
pid=$(cat /opt/service-pid)
while [ -z "$(cat /proc/$pid/task/$pid/children)" ]; do sleep 0.1; done
cat /proc/$pid/task/$pid/children > /opt/old-child
sh /opt/bundle/install.sh
''')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.base / 'premature').exists(), 'replaced a dependency while the old child was still alive')

    def test_failed_upgrade_start_restores_binary_bootlinks_and_running_service(self):
        self.put(self.base / 'fail-start', '2')
        self.put(self.root / 'etc/config/warpd', "# custom\nconfig warpd 'global'\n option enabled '1'\n", 0o600)
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
printf old >> /usr/bin/warpd
cp /usr/bin/warpd /opt/old-binary
mkdir -p /etc/init.d
cp /opt/bundle/files/warpd.init /etc/init.d/warpd
/etc/init.d/warpd enable
/etc/init.d/warpd start
: > /opt/service-events
sh /opt/bundle/install.sh
status=$?
[ "$status" != 0 ] && /etc/init.d/warpd running
''')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.root / 'usr/bin/warpd').read_bytes(), (self.base / 'old-binary').read_bytes())
        self.assertTrue((self.root / 'etc/rc.d/S99warpd').is_symlink())
        self.assertEqual((self.base / 'service-events').read_text().splitlines(), ['stop', 'start', 'stop', 'start'])
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_failed_fresh_start_removes_created_files_config_and_bootlinks(self):
        self.put(self.base / 'fail-start', '1')
        result = self.live('sh /opt/bundle/install.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('start failed', result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        self.assertFalse((self.root / 'etc/config/warpd').exists())
        self.assertFalse((self.root / 'etc/rc.d/S99warpd').is_symlink())
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_live_uninstaller_stops_and_disables_only_owned_service(self):
        result = self.live('sh /opt/bundle/install.sh && : > /opt/service-events && sh /usr/sbin/warpd-uninstall')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'service-events').read_text().splitlines(), ['stop', 'disable'])
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        self.assertTrue((self.root / 'etc/config/warpd').exists())
        self.assertFalse((self.root / 'etc/rc.d/S99warpd').is_symlink())

    def test_install_refuses_unowned_private_dependency_and_registered_package(self):
        self.put(self.root / 'usr/libexec/warpd/mihomo', 'unrelated private file', 0o755)
        before = self.snapshot()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.snapshot(), before)
        (self.root / 'usr/libexec/warpd/mihomo').unlink()
        self.put(self.base / 'live-tools/apk', '#!/bin/sh\n[ "$*" = "info -e warpd" ]\n', 0o755)
        result = self.live('sh /opt/bundle/install.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('package', result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())

    def test_rollback_failure_retains_original_backup_for_manual_recovery(self):
        self.put(self.root / 'usr/bin/warpd', 'OLD BINARY', 0o755)
        wrappers = self.base / 'wrappers'
        wrappers.mkdir()
        self.put(wrappers / 'mv', '''#!/bin/sh
n=0
[ ! -f "$COUNTER" ] || n=$(cat "$COUNTER")
n=$((n+1)); printf '%s' "$n" > "$COUNTER"
[ "$n" != 9 ] || exit 71
case "$*" in */old*) exit 72 ;; esac
exec REAL_MV "$@"
'''.replace('REAL_MV', shutil.which('mv')), 0o755)
        env = dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH'], COUNTER=str(self.base / 'counter'))
        result = self.run_script(env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('ROLLBACK FAILED', result.stderr)
        originals = [p for p in self.root.rglob('old') if p.read_bytes() == b'OLD BINARY']
        self.assertEqual(len(originals), 1, 'lost original backup after rollback rename failure')

    def test_external_dependency_cannot_be_a_path_installer_replaces(self):
        self.install()
        before = self.snapshot()
        for name in ('mihomo', 'warpscout'):
            result = self.run_script('install.sh', '--' + name, str(self.root / 'usr/libexec/warpd' / name))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('installer-owned', result.stderr)
            self.assertEqual(self.snapshot(), before)

    def test_root_slash_alias_cannot_bypass_live_mode_guards(self):
        result = self.live('sh /opt/bundle/install.sh --root //')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('root', result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        result = self.run_script('uninstall.sh', '--root', '///')
        self.assertNotEqual(result.returncode, 0)

    def test_root_with_journal_separator_is_rejected_before_changes(self):
        self.root = self.base / 'unsafe|root'
        self.root.mkdir()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Unsafe root', result.stderr)
        self.assertEqual(self.snapshot(), {})

    def test_missing_live_uci_is_detected_before_installation(self):
        result = self.live('chmod -x /media/uci; sh /opt/bundle/install.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('prerequisite: uci', result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())

    def test_busybox_ash_offline_lifecycle(self):
        busybox = os.environ.get('WARPD_TEST_BUSYBOX') or shutil.which('busybox')
        if not busybox:
            self.skipTest('BusyBox unavailable; set WARPD_TEST_BUSYBOX to exercise ash and applets')
        applets = self.base / 'applets'
        applets.mkdir()
        subprocess.run([busybox, '--install', '-s', str(applets)], check=True, capture_output=True)
        env = dict(os.environ, PATH=str(applets))
        shell = [busybox, 'ash']
        self.install(env=env, shell=shell)
        self.install(env=env, shell=shell)
        result = self.run_script('uninstall.sh', '--purge', env=env, shell=shell)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        self.assertFalse((self.root / 'etc/config/warpd').exists())

    def no_stat_environment(self, busybox=False):
        """Force command lookup through a minimal PATH, including BusyBox tools.

        Use the host POSIX shell deliberately: some BusyBox ash builds resolve
        applets absent from PATH internally, hiding the router's missing stat.
        The separate ash lifecycle test still exercises the actual ash shell.
        """
        tools = self.base / ('no-stat-busybox' if busybox else 'no-stat-host')
        tools.mkdir(exist_ok=True)
        binary = None
        if busybox:
            binary = os.environ.get('WARPD_TEST_BUSYBOX') or shutil.which('busybox')
            if not binary:
                self.skipTest('BusyBox unavailable; set WARPD_TEST_BUSYBOX')
            binary = str(Path(binary).resolve())
        commands = 'sh sha256sum cat cp mv chmod mkdir dirname uname id readlink grep df awk mktemp rm rmdir ln sleep wc ls'
        for name in commands.split():
            target = tools / name
            if not target.exists():
                executable = binary or shutil.which(name)
                self.assertIsNotNone(executable, 'test prerequisite unavailable: ' + name)
                assert executable is not None
                target.symlink_to(executable)
        # sh must honor PATH even when all other commands are BusyBox applets.
        (tools / 'sh').unlink()
        (tools / 'sh').symlink_to('/bin/sh')
        env = dict(os.environ, PATH=str(tools), LC_ALL='C')
        probe = subprocess.run(['/bin/sh', '-c', 'command -v stat'], env=env, text=True, capture_output=True)
        self.assertNotEqual(probe.returncode, 0, 'test PATH unexpectedly provides stat')
        self.assertEqual(probe.stdout, '')
        return env, tools, binary

    def test_fresh_offline_install_without_stat(self):
        for busybox in (False, True):
            with self.subTest(busybox=busybox):
                self.root = self.base / ('fresh-busybox' if busybox else 'fresh-host')
                self.root.mkdir()
                env, _, _ = self.no_stat_environment(busybox)
                result = self.install(env=env, shell='/bin/sh')
                self.assertIn('not started', result.stdout)
                self.assertEqual((self.root / 'etc/warpd').stat().st_mode & 0o7777, 0o700)
                self.assertEqual((self.root / 'etc/config/warpd').stat().st_mode & 0o7777, 0o600)
                self.assertTrue((self.root / 'usr/bin/warpd').is_file())

    def test_offline_upgrade_without_stat_preserves_config_account(self):
        for busybox in (False, True):
            with self.subTest(busybox=busybox):
                self.root = self.base / ('upgrade-busybox' if busybox else 'upgrade-host')
                self.root.mkdir()
                env, _, _ = self.no_stat_environment(busybox)
                self.install(env=env, shell='/bin/sh')
                config = self.root / 'etc/config/warpd'
                account = self.root / 'etc/warpd/warp-account.json'
                self.put(config, '# PRIVATE custom config\n', 0o600)
                self.put(account, 'PRIVATE TEST ACCOUNT\n', 0o640)
                account.parent.chmod(0o2750)
                self.put(self.root / 'usr/bin/warpd', 'OLD TEST BINARY', 0o751)
                self.install(env=env, shell='/bin/sh')
                self.assertEqual(config.read_bytes(), b'# PRIVATE custom config\n')
                self.assertEqual(account.read_bytes(), b'PRIVATE TEST ACCOUNT\n')
                self.assertEqual(account.stat().st_mode & 0o7777, 0o640)
                # Preserve chmod 700 semantics (GNU chmod retains directory setgid).
                self.assertEqual(account.parent.stat().st_mode & 0o777, 0o700)
                self.assertIn('TEST fixture', (self.root / 'usr/bin/warpd').read_text())
                result = self.run_script('uninstall.sh', env=env, shell='/bin/sh')
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(config.read_bytes(), b'# PRIVATE custom config\n')
                self.assertEqual(account.read_bytes(), b'PRIVATE TEST ACCOUNT\n')

    def test_offline_rollback_without_stat_restores_directory_special_bits(self):
        for busybox in (False, True):
            env, tools, binary = self.no_stat_environment(busybox)
            real_chmod = (binary + ' chmod') if binary else shutil.which('chmod')
            assert real_chmod is not None
            (tools / 'chmod').unlink()
            self.put(tools / 'chmod', '''#!/bin/sh
REAL_CHMOD "$@" || exit
if [ "$1" = 700 ] && [ "$2" = "$ACCOUNT_DIR" ] && [ ! -e "$CHMOD_FAILED" ]; then
    : > "$CHMOD_FAILED"
    printf '%s\\n' 'TEST failure after account chmod' >&2
    exit 71
fi
'''.replace('REAL_CHMOD', real_chmod), 0o755)
            for mode in (0o755, 0o750, 0o1700, 0o2640, 0o4700, 0o7750, 0o7644):
                with self.subTest(busybox=busybox, mode=oct(mode)):
                    self.root = self.base / ('rollback-' + str(busybox) + '-' + oct(mode))
                    self.root.mkdir()
                    self.put(self.root / 'usr/bin/warpd', 'OLD TEST BINARY', 0o751)
                    self.put(self.root / 'etc/config/warpd', '# original config\n', 0o640)
                    account = self.root / 'etc/warpd/warp-account.json'
                    self.put(account, 'PRIVATE TEST ACCOUNT\n', 0o640)
                    original_mode = account.parent.stat().st_mode & 0o7777
                    before = self.snapshot()
                    account.parent.chmod(mode)
                    self.assertEqual(account.parent.stat().st_mode & 0o7777, mode)
                    marker = self.base / ('chmod-failed-' + str(busybox) + '-' + oct(mode))
                    result = self.run_script(env=dict(env, ACCOUNT_DIR=str(account.parent), CHMOD_FAILED=str(marker)), shell='/bin/sh')
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertIn('TEST failure after account chmod', result.stderr)
                    self.assertIn('previous files restored', result.stderr)
                    self.assertEqual(account.parent.stat().st_mode & 0o7777, mode)
                    # Restore traversal for non-root inspection of uppercase S/T cases.
                    account.parent.chmod(original_mode)
                    self.assertEqual(self.snapshot(), before)
                    self.assertEqual(account.stat().st_mode & 0o7777, 0o640)

    def test_stop_wait_includes_children_spawned_by_nonleader_threads(self):
        if not shutil.which('cc'):
            self.skipTest('C compiler unavailable for multithreaded process fixture')
        daemon = self.base / 'thread-daemon'
        code = r'''#include <pthread.h>
#include <unistd.h>
#include <stdio.h>
static void *worker(void *unused) {
    (void)unused;
    int child = fork();
    if (child == 0) { execl("/mnt/sleep", "sleep", "1000", (char *)0); _exit(1); }
    FILE *f = fopen("/opt/thread-child", "w");
    fprintf(f, "%d", child); fclose(f);
    for (;;) pause();
    return 0;
}
int main(void) { pthread_t thread; pthread_create(&thread, 0, worker, 0); for (;;) pause(); }
'''
        compiled = subprocess.run(['cc', '-pthread', '-x', 'c', '-o', str(daemon), '-'], input=code, text=True, capture_output=True)
        self.assertEqual(compiled.returncode, 0, compiled.stderr)
        self.put(self.base / 'slow-stop', '1')
        self.put(self.base / 'live-tools/mv', '''#!/bin/sh
case "$*" in */usr/libexec/warpd/mihomo*)
 pid=$(cat /opt/old-child)
 if kill -0 "$pid" 2>/dev/null && ! grep -q '^State:.*Z' "/proc/$pid/status"; then echo bad > /opt/premature; fi ;;
esac
exec /mnt/mv "$@"
''', 0o755)
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
mkdir -p /etc/init.d
cp /opt/bundle/files/warpd.init /etc/init.d/warpd
/etc/init.d/warpd start
while [ ! -s /opt/thread-child ]; do sleep 0.1; done
cp /opt/thread-child /opt/old-child
sh /opt/bundle/install.sh
''', daemon=daemon)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.base / 'premature').exists(), 'missed the child created by a nonleader thread')

    def test_running_manual_daemon_without_init_is_not_overwritten(self):
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
printf old >> /usr/bin/warpd
cp /usr/bin/warpd /opt/old-binary
/usr/bin/warpd -c 'sleep 1000' >/dev/null 2>&1 &
sleep 0.1
sh /opt/bundle/install.sh
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Running warpd', result.stderr)
        self.assertEqual((self.root / 'usr/bin/warpd').read_bytes(), (self.base / 'old-binary').read_bytes())
        self.assertFalse((self.root / 'usr/libexec/warpd/mihomo').exists())

    def test_disk_budget_includes_both_preserved_config_copies(self):
        self.put(self.root / 'etc/config/warpd', '#' * (3 * 1024 * 1024), 0o600)
        before = self.snapshot()
        wrappers = self.base / 'wrappers'
        self.put(wrappers / 'df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 8192 4096 4096 50%% /\\n"\n', 0o755)
        result = self.run_script(env=dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH']))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('space', result.stderr)
        self.assertEqual(self.snapshot(), before)

    def test_uninstaller_refuses_foreign_bootlink_before_stopping_service(self):
        result = self.live('''sh /opt/bundle/install.sh || exit
ln -sf ../init.d/other /etc/rc.d/S99warpd
: > /opt/service-events
sh /usr/sbin/warpd-uninstall
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / 'usr/bin/warpd').exists())
        self.assertEqual(os.readlink(self.root / 'etc/rc.d/S99warpd'), '../init.d/other')
        self.assertEqual((self.base / 'service-events').read_text(), '')

    def test_uninstaller_refuses_running_daemon_with_missing_init(self):
        result = self.live('sh /opt/bundle/install.sh && rm /etc/init.d/warpd && sh /usr/sbin/warpd-uninstall')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Running warpd', result.stderr)
        self.assertTrue((self.root / 'usr/bin/warpd').exists())
        self.assertTrue((self.root / 'usr/libexec/warpd/mihomo').exists())

    def test_external_dependencies_do_not_budget_unused_bundle_binary_copies(self):
        external = self.base / 'external'
        self.put(external, '#!/bin/sh\nexit 0\n', 0o755)
        for name in ('mihomo', 'warpscout'):
            self.put(self.bundle / 'bin' / name, '#!/bin/sh\nexit 0\n# TEST fixture ' + '#' * (1024 * 1024), 0o755)
        self.checksums()
        wrappers = self.base / 'wrappers'
        self.put(wrappers / 'df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 1024 512 512 50%% /\\n"\n', 0o755)
        env = dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH'])
        self.install('--mihomo', str(external), '--warpscout', str(external), env=env)

    def test_live_disabled_upgrade_preserves_autostart_and_never_starts(self):
        config = "# byte-preserved disabled config\nconfig warpd 'global'\n option enabled '0'\n"
        self.put(self.root / 'etc/config/warpd', config, 0o600)
        self.put(self.root / 'etc/warpd/warp-account.json', 'PRIVATE TEST ACCOUNT', 0o600)
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
mkdir -p /etc/init.d
cp /opt/bundle/files/warpd.init /etc/init.d/warpd
/etc/init.d/warpd enable
: > /opt/service-events
sh /opt/bundle/install.sh
''')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'service-events').read_text(), '')
        self.assertTrue((self.root / 'etc/rc.d/S99warpd').is_symlink())
        self.assertEqual((self.root / 'etc/config/warpd').read_text(), config)
        self.assertEqual((self.root / 'etc/warpd/warp-account.json').read_text(), 'PRIVATE TEST ACCOUNT')

    def test_live_enabled_but_stopped_upgrade_is_not_started(self):
        self.put(self.root / 'etc/config/warpd', "config warpd 'global'\n option enabled '1'\n", 0o600)
        result = self.live('''cp /opt/bundle/bin/warpd /usr/bin/warpd
mkdir -p /etc/init.d
cp /opt/bundle/files/warpd.init /etc/init.d/warpd
: > /opt/service-events
sh /opt/bundle/install.sh
''')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'service-events').read_text(), '')
        self.assertFalse((self.root / 'etc/rc.d/S99warpd').is_symlink())

    def test_live_arch_mismatch_fails_without_changes(self):
        self.put(self.base / 'live-tools/uname', '#!/bin/sh\necho unsupported-architecture\n', 0o755)
        result = self.live('sh /opt/bundle/install.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Architecture mismatch', result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())

    def test_live_nonroot_fails_without_changes(self):
        self.put(self.base / 'live-tools/id', '#!/bin/sh\necho 1000\n', 0o755)
        result = self.live('sh /opt/bundle/install.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Root privileges', result.stderr)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())

    def test_uninstaller_invalid_args_and_symlink_escape_are_nonmutating(self):
        self.install()
        before = self.snapshot()
        for args in (('--wat',), ('--root',), ('--root', 'relative'), ('--purge', '--purge')):
            result = self.run_script('uninstall.sh', *args)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(self.snapshot(), before)
        outside = self.base / 'outside-usr'
        (self.root / 'usr').rename(outside)
        (self.root / 'usr').symlink_to(outside, target_is_directory=True)
        result = self.run_script('uninstall.sh', '--purge')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((outside / 'bin/warpd').exists())
        self.assertTrue((self.root / 'etc/config/warpd').exists())

    def test_offline_mode_never_invokes_host_uci_package_or_service_tools(self):
        wrappers = self.base / 'wrappers'
        for name in ('uci', 'ubus', 'apk', 'opkg', 'service'):
            self.put(wrappers / name, '#!/bin/sh\necho called >> "$TRIPWIRE"\nexit 90\n', 0o755)
        env = dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH'], TRIPWIRE=str(self.base / 'tripwire'))
        self.install(env=env)
        result = self.run_script('uninstall.sh', env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.base / 'tripwire').exists())

    def test_start_returning_success_without_running_daemon_is_rolled_back(self):
        self.put(self.base / 'no-daemon', '1')
        result = self.live('sh /opt/bundle/install.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('failed to remain running', result.stderr)
        self.assertNotIn('Installed warpd', result.stdout)
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        self.assertFalse((self.root / 'etc/rc.d/S99warpd').is_symlink())

    def test_dependency_mode_changes_do_not_modify_external_targets(self):
        external = self.base / 'external'
        content = '#!/bin/sh\n# TEST external executable\nexit 0\n'
        self.put(external, content, 0o755)
        self.install('--mihomo', str(external), '--warpscout', str(external))
        self.install()
        self.assertFalse((self.root / 'usr/libexec/warpd/mihomo').is_symlink())
        self.assertEqual(external.read_text(), content)
        self.install('--mihomo', str(external), '--warpscout', str(external))
        self.assertTrue((self.root / 'usr/libexec/warpd/mihomo').is_symlink())
        self.assertEqual(external.read_text(), content)


if __name__ == '__main__':
    unittest.main()
