"""Offline execution of the real online bootstrap in a network-isolated mount namespace.
Only the download/package boundaries are test doubles; SHA256, tar and shell are real.
Run: python3 -m unittest discover -s packaging -p bootstrap_test.py -v
TDD ledger: each numbered implementation slice was observed failing against the
real shell before its fix, then passed with the listed cumulative suite size:
1 check-only; 2 online invocation; 3 option guards; 4 requirements;
5 apk/opkg opt-in; 6 interactive consent; 7 UI-language selection;
8 external arguments; 9 exact outer checksum row; 10 archive allowlist;
11 RAM/overlay space; 12 downloader compatibility; 13 clock; 14 fresh-temp ownership;
19 procd shell library; 20 streaming file cap; 21 discard compressed download;
23 multiline semver; 24 procd helpers; 25 shared-filesystem peak;
26 release metadata; 30 completion only after cleanup; 32 limit-setup failure.
Counts 15-18, 22, 27-29 and 31 include regression/compatibility additions (package and
platform failures, BusyBox ash/tools, actual builder output and cached v2.1.1,
data/config preservation and a 39 MiB overlay with explicit reuse).
Deadline regression: wget-only preflight was observed failing for both missing
curl consent and check-only; curl is now required for bounded asset downloads.
All payloads/adapters here are TEST fixtures, never release binaries. Download
and package boundaries are isolated, not exposed as production overrides.
"""
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import time
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'install.sh'
TOOLS = 'sh id uname cat cp mv chmod mkdir dirname readlink grep wc ls df awk mktemp rm rmdir ln sleep date tar gzip sha256sum'.split()


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        if not shutil.which('bwrap'):
            self.skipTest('bubblewrap required for safely isolated bootstrap')
        self.tmp = tempfile.TemporaryDirectory(prefix='warpd-bootstrap-', dir=os.environ.get('TMPDIR'))
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / 'root'
        for directory in ('etc/config', 'etc/ssl/certs', 'tmp', 'tools', 'lib/functions', 'usr/libexec/warpd'):
            (self.root / directory).mkdir(parents=True, exist_ok=True)
        for entry in Path('/usr/lib').iterdir():
            if entry.name != 'functions':
                (self.root / 'lib' / entry.name).symlink_to('/mnt/' + entry.name)
        for tool in TOOLS:
            executable = shutil.which(tool)
            self.assertIsNotNone(executable, 'test prerequisite: ' + tool)
            assert executable is not None
            (self.root / 'tools' / tool).symlink_to(Path(executable).resolve())
        self.put('etc/openwrt_release', "DISTRIB_ID='OpenWrt'\n")
        self.put('etc/ssl/certs/ca-certificates.crt', 'TEST trust-store fixture\n')
        self.put('etc/rc.common', '# TEST base-files fixture\n')
        self.put('lib/functions/procd.sh', '# TEST procd fixture\n')
        for tool in ('procd', 'ubus', 'jsonfilter'):
            self.put('tools/' + tool, '#!/bin/sh\nexit 0\n', 0o755)
        self.put('tools/uci', '#!/bin/sh\nexit 0\n', 0o755)
        self.put('tools/curl', '''#!/bin/sh
printf 'curl %s\n' "$*" >> /opt/events
out=
while [ "$#" -gt 0 ]; do
 case "$1" in -o) out=$2; shift 2;; *) url=$1; shift;; esac
done
[ ! -f /opt/fail-download ] || exit 22
case "$url" in
 https://github.com/LirikKun/warpd-openwrt/releases/download/v3.0.0/warpd-3.0.0-linux-*.tar.gz.sha256) cp /opt/SHA256SUMS "$out";;
 https://github.com/LirikKun/warpd-openwrt/releases/download/v3.0.0/warpd-3.0.0-linux-*.tar.gz) cp /opt/bundle.tar.gz "$out";;
 *) exit 93;;
esac
''', 0o755)
        self.bundle()
        self.put('tmp/operator-sentinel', 'keep me')

    def put(self, path, data, mode=0o644):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.is_symlink():
            target.unlink()
        target.write_text(data)
        target.chmod(mode)

    def bundle(self, extra=None):
        self.arch = 'amd64' if os.uname().machine == 'x86_64' else 'arm64'
        self.name = 'warpd-3.0.0-linux-' + self.arch
        files = {name: b'TEST fixture\n' for name in ('uninstall.sh', 'bin/warpd', 'bin/mihomo', 'bin/warpscout', 'files/warpd.init', 'files/warpd.config', 'README.md', 'THIRD_PARTY.md', 'licenses/warpscout-MIT.txt', 'licenses/mihomo-GPL-3.0.txt')}
        files.update({'VERSION': b'3.0.0\n', 'ARCH': (self.arch + '\n').encode(), 'install.sh': b'#!/bin/sh\nprintf "%s\\n" "$@" > /opt/invoked\n[ ! -f /opt/fail-install ]\n'})
        files['SHA256SUMS'] = ''.join(hashlib.sha256(data).hexdigest() + '  ' + name + '\n' for name, data in sorted(files.items())).encode()
        with tarfile.open(self.base / 'bundle.tar.gz', 'w:gz', format=tarfile.USTAR_FORMAT) as tar:
            for directory in ('', '/bin', '/files', '/licenses'):
                member = tarfile.TarInfo(self.name + directory)
                member.type = tarfile.DIRTYPE
                member.mode = 0o755
                tar.addfile(member)
            for name, data in files.items():
                member = tarfile.TarInfo(self.name + '/' + name)
                member.size = len(data)
                tar.addfile(member, io.BytesIO(data))
            if extra:
                tar.addfile(extra)
        digest = hashlib.sha256((self.base / 'bundle.tar.gz').read_bytes()).hexdigest()
        (self.base / 'SHA256SUMS').write_text(digest + '  ' + self.name + '.tar.gz\n')

    def run_script(self, *flags, tty_input=None):
        cmd = ['bwrap', '--unshare-user', '--uid', '0', '--gid', '0', '--unshare-pid', '--unshare-net', '--die-with-parent', '--ro-bind', '/', '/', '--ro-bind', '/usr/lib', '/mnt', '--bind', str(self.root / 'lib'), '/usr/lib', '--bind', str(self.base), '/opt', '--bind', str(self.root / 'etc'), '/etc', '--bind', str(self.root / 'tmp'), '/tmp', '--bind', str(self.root / 'usr/libexec'), '/usr/libexec', '--bind', str(self.root / 'tools'), '/media', '--proc', '/proc', '--dev', '/dev', '--setenv', 'PATH', '/media', *getattr(self, 'shell', ['/bin/sh']), str(SCRIPT), *flags]
        if tty_input is None:
            result = subprocess.run(cmd, input='', text=True, capture_output=True, timeout=25)
        else:
            master, slave = pty.openpty()
            process = subprocess.Popen(cmd, stdin=slave, stdout=slave, stderr=slave)
            os.close(slave)
            os.write(master, tty_input.encode())
            output = bytearray()
            deadline = time.monotonic() + 25
            while process.poll() is None:
                if time.monotonic() >= deadline:
                    process.kill()
                    process.wait()
                    os.close(master)
                    self.fail('interactive bootstrap did not finish within 25 seconds: ' + output.decode())
                if select.select([master], [], [], 1)[0]:
                    try:
                        output.extend(os.read(master, 65536))
                    except OSError:
                        break
            process.wait(timeout=25)
            try:
                while True:
                    output.extend(os.read(master, 65536))
            except OSError:
                pass
            os.close(master)
            result = subprocess.CompletedProcess(cmd, process.returncode, output.decode(), '')
        if 'Operation not permitted' in result.stderr or 'No permissions to' in result.stderr:
            self.skipTest('user namespaces unavailable: ' + result.stderr)
        return result

    def clean(self):
        self.assertEqual(sorted(p.name for p in (self.root / 'tmp').iterdir()), ['operator-sentinel'])
        self.assertEqual((self.root / 'tmp/operator-sentinel').read_text(), 'keep me')

    def assert_bounded_downloads(self, events):
        downloads = [shlex.split(row) for row in events if row.startswith('curl ')]
        self.assertEqual(len(downloads), 2, events)
        for args, suffix in zip(downloads, ('.tar.gz.sha256', '.tar.gz'), strict=True):
            self.assertEqual(args[:-2], ['curl', '-fL', '--proto', '=https', '--proto-redir', '=https',
                                         '--connect-timeout', '15', '--max-time', '180', '-o'])
            self.assertTrue(args[-2].startswith('/tmp/warpd-bootstrap.'), args)
            self.assertEqual(args[-1], 'https://github.com/LirikKun/warpd-openwrt/releases/download/v3.0.0/' + self.name + suffix)

    def test_fresh_download_verifies_and_invokes_exact_release(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.base / 'invoked').exists(), 'bundle installer was not invoked')
        events = (self.base / 'events').read_text()
        self.assertIn('/v3.0.0/' + self.name + '.tar.gz', events)
        self.assertIn('--proto =https', events)
        self.assert_bounded_downloads(events.splitlines())
        self.assertIn('complete', result.stdout.lower())
        self.clean()

    def test_options_validate_before_network_or_mutation(self):
        for flags in (('--version', 'v3.0.0'), ('--version', '03.0.0'), ('--version', '3.0.0;id'), ('--version', '2.2'), ('--language', 'de'), ('--language',), ('--mihomo', 'relative'), ('--root', '/opt'), ('--version', '3.0.0', '--version', '3.0.0')):
            with self.subTest(flags=flags):
                result = self.run_script(*flags)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse((self.base / 'events').exists())
                self.clean()
        result = self.run_script('--version', '3.0.0', '--check-only')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_missing_requirements_report_packages_without_changes(self):
        for name in ('curl', 'sha256sum', 'uci', 'procd', 'tar', 'gzip'):
            (self.root / 'tools' / name).unlink()
        (self.root / 'etc/ssl/certs/ca-certificates.crt').unlink()
        result = self.run_script('--check-only', '--yes')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        for package in ('curl', 'coreutils-sha256sum', 'uci', 'procd', 'tar', 'gzip', 'ca-bundle'):
            self.assertIn(package, result.stdout + result.stderr)
        self.assertFalse((self.base / 'events').exists())
        self.clean()

    def package_manager(self, manager):
        self.put('tools/' + manager, '''#!/bin/sh
printf 'PKG %s\\n' "$*" >> /opt/events
case "$1" in
 update) [ ! -f /opt/fail-update ] ;;
 add|install)
  [ ! -f /opt/fail-package ] || exit 42
  [ ! -f /opt/postcheck-failure ] || exit 0
  shift
  for package in "$@"; do
   case "$package" in
    curl) cp /opt/saved-curl /media/curl; chmod 755 /media/curl;;
    ca-bundle) printf TEST > /etc/ssl/certs/ca-certificates.crt;;
    coreutils-sha256sum) ln -s /usr/bin/sha256sum /media/sha256sum;;
    uci|procd) printf '#!/bin/sh\\nexit 0\\n' > /media/"$package"; chmod 755 /media/"$package";;
    *) exit 94;;
   esac
  done ;;
 *) exit 1;;
esac
''', 0o755)

    def missing_network_requirements(self):
        shutil.copy2(self.root / 'tools/curl', self.base / 'saved-curl')
        (self.root / 'tools/curl').unlink()
        (self.root / 'etc/ssl/certs/ca-certificates.crt').unlink()
        self.put('tools/wget', '#!/bin/sh\nprintf "UNEXPECTED wget\\n" >> /opt/events\nexit 97\n', 0o755)

    def test_yes_installs_only_missing_packages_with_apk_and_opkg(self):
        self.missing_network_requirements()
        for manager, action in (('apk', 'add'), ('opkg', 'install')):
            with self.subTest(manager=manager):
                self.package_manager(manager)
                result = self.run_script('--yes')
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                events = (self.base / 'events').read_text().splitlines()
                self.assertEqual(events[:2], ['PKG update', 'PKG ' + action + ' curl ca-bundle'])
                self.assertEqual(len(events), 4, events)
                self.assert_bounded_downloads(events)
                self.assertTrue((self.base / 'invoked').exists())
                self.clean()
                (self.root / 'tools' / manager).unlink()
                (self.root / 'tools/curl').unlink()
                (self.root / 'etc/ssl/certs/ca-certificates.crt').unlink()
                (self.base / 'events').unlink()
                (self.base / 'invoked').unlink()

    def test_interactive_package_consent_and_noninteractive_refusal(self):
        self.missing_network_requirements()
        self.package_manager('apk')
        for reply in (None, 'n\n', '\x04'):
            result = self.run_script(tty_input=reply)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse((self.base / 'events').exists())
            self.clean()
        result = self.run_script('--language', 'en', tty_input='y\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Установить', result.stdout)
        self.assertTrue((self.base / 'invoked').exists())

    def test_language_selection_is_forwarded_but_upgrade_blank_preserves(self):
        for flags, reply, expected in ((('--language', 'ru'), None, ['--language', 'ru']), ((), '\n', ['--language', 'en']), ((), '2\n', ['--language', 'ru']), ((), '\x04', ['--language', 'en'])):
            with self.subTest(flags=flags, reply=reply):
                result = self.run_script(*flags, tty_input=reply)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual((self.base / 'invoked').read_text().splitlines(), expected)
        self.put('etc/config/warpd', '# existing Russian config; retain bytes\n')
        for reply in (None, '\n', '\x04'):
            result = self.run_script(tty_input=reply)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((self.base / 'invoked').read_text(), '\n')
        result = self.run_script('--language', 'en')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.base / 'invoked').read_text().splitlines(), ['--language', 'en'])

    def test_external_dependencies_forward_exact_args_and_reject_owned_paths_early(self):
        self.put('tmp/shared mihomo;sentinel', '#!/bin/sh\nexit 0\n', 0o755)
        self.put('tmp/scout', '#!/bin/sh\nexit 0\n', 0o755)
        result = self.run_script('--mihomo', '/tmp/shared mihomo;sentinel', '--warpscout', '/tmp/scout')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'invoked').read_text().splitlines(), ['--mihomo', '/tmp/shared mihomo;sentinel', '--warpscout', '/tmp/scout'])
        (self.base / 'events').unlink()
        for target in ('/usr/libexec/warpd/mihomo', '/tmp/missing'):
            self.put('usr/libexec/warpd/mihomo', '#!/bin/sh\nexit 0\n', 0o755)
            result = self.run_script('--mihomo', target)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse((self.base / 'events').exists())

    def test_outer_manifest_accepts_other_assets_but_checks_exact_selected_row(self):
        manifest = self.base / 'SHA256SUMS'
        original = manifest.read_text()
        manifest.write_text(original + 'a' * 64 + '  warpd-3.0.0-openwrt-other.tar.gz\n')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for bad in (original + original, original.replace(self.name + '.tar.gz', '../escape'), original.replace('  ', ' extra '), original.replace(original[:64], '0' * 64)):
            manifest.write_text(bad)
            (self.base / 'invoked').unlink(missing_ok=True)
            result = self.run_script()
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse((self.base / 'invoked').exists())
            self.clean()

    def test_archive_allowlist_rejects_traversal_links_special_and_extra_members(self):
        for path, kind in (('../escape', tarfile.REGTYPE), (self.name + '/unknown', tarfile.REGTYPE), (self.name + '/link', tarfile.SYMTYPE), (self.name + '/hardlink', tarfile.LNKTYPE), (self.name + '/fifo', tarfile.FIFOTYPE), (self.name + '/ARCH', tarfile.REGTYPE)):
            with self.subTest(path=path, kind=kind):
                member = tarfile.TarInfo(path)
                member.type = kind
                member.linkname = '/tmp/operator-sentinel' if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else ''
                self.bundle(extra=member)
                (self.base / 'invoked').unlink(missing_ok=True)
                result = self.run_script()
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse((self.base / 'invoked').exists())
                self.clean()

    def test_space_preflight_blocks_before_download_and_rechecks_before_extract(self):
        for location in ('/tmp', '/usr'):
            self.put('tools/df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\n"\ncase "$2" in ' + location + ') n=1;; *) n=999999;; esac\nprintf "fixture 999999 0 %s 0%% %s\\n" "$n" "$2"\n', 0o755)
            result = self.run_script()
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertIn('space', result.stderr.lower())
            self.assertFalse((self.base / 'events').exists())
            self.clean()
        self.put('tools/df', '''#!/bin/sh
n=999999
[ ! -f /opt/events ] || n=1
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 %s 0%% %s\\n' "$n" "$2"
''', 0o755)
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('space', result.stderr.lower())
        self.assertFalse((self.base / 'invoked').exists())
        self.clean()

    def test_wget_available_without_curl_requires_consent_before_any_changes(self):
        (self.root / 'tools/curl').rename(self.base / 'saved-curl')
        self.put('tools/wget', '''#!/bin/sh
printf 'wget %s\\n' "$*" >> /opt/events
exit 97
''', 0o755)
        self.package_manager('apk')
        before = {p.relative_to(self.root): p.read_bytes()
                  for p in self.root.rglob('*') if p.is_file()}
        for flags in ((), ('--check-only', '--yes')):
            with self.subTest(flags=flags):
                result = self.run_script(*flags)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn('Missing required packages: curl', result.stdout)
                self.assertIn('rerun with --yes' if not flags else 'check-only made no changes', result.stderr)
                self.assertFalse((self.base / 'events').exists(), 'no wget, curl or package operation permitted')
                self.assertFalse((self.base / 'invoked').exists())
                self.assertEqual({p.relative_to(self.root): p.read_bytes()
                                  for p in self.root.rglob('*') if p.is_file()}, before)
                self.clean()

    def test_bad_clock_and_platform_are_rejected_before_packages_or_downloads(self):
        self.put('tools/date', '#!/bin/sh\nprintf "123\\n"\n', 0o755)
        result = self.run_script('--yes')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('clock', result.stderr.lower())
        self.assertFalse((self.base / 'events').exists())
        self.clean()

    def test_private_temp_creation_is_verified_before_cleanup(self):
        self.put('tmp/warpd-bootstrap.operator/precious', 'operator data')
        self.put('tools/mktemp', '#!/bin/sh\nprintf "/tmp/warpd-bootstrap.operator\\n"\n', 0o755)
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual((self.root / 'tmp/warpd-bootstrap.operator/precious').read_text(), 'operator data')
        self.assertFalse((self.base / 'events').exists())

    def test_package_failures_and_postcheck_prevent_any_bundle_download(self):
        self.missing_network_requirements()
        self.package_manager('apk')
        for marker, expected in (('fail-update', 'index'), ('fail-package', 'installation failed'), ('postcheck-failure', 'post-check')):
            with self.subTest(marker=marker):
                (self.base / marker).touch()
                (self.base / 'events').unlink(missing_ok=True)
                result = self.run_script('--yes')
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn(expected, result.stderr)
                if marker == 'postcheck-failure':
                    self.assertIn('still missing: curl ca-bundle', result.stderr)
                self.assertIn('NOT rolled back', result.stdout)
                self.assertFalse(any(row.startswith('curl ') for row in (self.base / 'events').read_text().splitlines()))
                self.assertNotIn('wget', (self.base / 'events').read_text())
                self.assertFalse((self.base / 'invoked').exists())
                self.clean()
                (self.base / marker).unlink()

    def test_download_and_bundle_failure_are_fatal_without_false_completion(self):
        for marker in ('fail-download', 'fail-install'):
            with self.subTest(marker=marker):
                (self.base / marker).touch()
                result = self.run_script()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn('complete', result.stdout.lower())
                self.clean()
                (self.base / marker).unlink()
        lines = (self.base / 'events').read_text().splitlines()
        self.assertEqual(sum('.tar.gz.sha256' in row for row in lines), 4)

    def test_platform_root_arch_and_foundational_utilities_block_before_changes(self):
        for command, output in (('uname', 'mips'), ('id', '1000')):
            self.put('tools/' + command, '#!/bin/sh\nprintf "%s\\n" ' + output + '\n', 0o755)
            result = self.run_script('--yes')
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse((self.base / 'events').exists())
            (self.root / 'tools' / command).unlink()
            executable = shutil.which(command)
            assert executable is not None
            (self.root / 'tools' / command).symlink_to(Path(executable).resolve())
        (self.root / 'tools/cp').unlink()
        result = self.run_script('--yes')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('foundational utility: cp', result.stderr)
        self.assertFalse((self.base / 'events').exists())
        (self.root / 'etc/openwrt_release').unlink()
        result = self.run_script('--yes')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('OpenWrt required', result.stderr)
        self.clean()

    def test_busybox_tools_and_ash_execute_download_verify_extract(self):
        busybox = os.environ.get('WARPD_TEST_BUSYBOX') or shutil.which('busybox')
        if not busybox:
            self.skipTest('set WARPD_TEST_BUSYBOX to exercise target BusyBox applets')
        binary = Path(busybox).resolve()
        self.shell = [str(binary), 'ash']
        self.put('tools/sh', '#!/bin/sh\nexec ' + str(binary) + ' ash "$@"\n', 0o755)
        for tool in TOOLS:
            if tool == 'sh':
                continue
            self.put('tools/' + tool, '#!/bin/sh\nexec ' + str(binary) + ' ' + tool + ' "$@"\n', 0o755)
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.base / 'invoked').exists())
        self.clean()

    def test_missing_procd_shell_library_is_detected_and_postchecked(self):
        (self.root / 'lib/functions/procd.sh').unlink()
        result = self.run_script('--check-only')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('procd', result.stdout + result.stderr)
        self.clean()

    def test_streaming_download_is_capped_to_the_checked_ram_budget(self):
        self.put('tools/curl', '''#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in -o) out=$2; shift 2;; *) url=$1; shift;; esac
done
case "$url" in *.sha256) cp /opt/SHA256SUMS "$out";;
 *) awk 'BEGIN {s=""; for(i=0;i<1024;i++) s=s "x"; for(i=0;i<45000;i++) print s}' > "$out";;
esac
''', 0o755)
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('download failed', result.stderr)
        self.assertFalse((self.base / 'invoked').exists())
        self.clean()

    def test_archive_download_is_removed_before_bundle_transaction(self):
        original = (self.root / 'tools/curl').read_text()
        self.put('tools/curl', original.replace('cp /opt/bundle.tar.gz "$out"', 'cp /opt/bundle.tar.gz "$out"; printf "%s\\n" "$out" > /opt/archive-path'), 0o755)
        self.put('tools/sh', '''#!/bin/sh
archive=$(cat /opt/archive-path)
[ ! -e "$archive" ] || { printf 'compressed archive still occupies RAM\\n' >&2; exit 99; }
exec /bin/sh "$@"
''', 0o755)
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.clean()

    def test_real_release_builder_bundle_fresh_and_upgrade_preserve_data(self):
        spec = importlib.util.spec_from_file_location('release_builder', SCRIPT.parent / 'tools/build_release.py')
        assert spec is not None and spec.loader is not None
        builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(builder)
        binaries = {}
        for name in ('warpd', 'mihomo', 'warpscout'):
            target = self.base / ('test-' + name)
            target.write_text('#!/bin/sh\n# TEST payload\nexit 0\n')
            binaries[name] = target
        archive = builder.create_archive(SCRIPT.parent, binaries, self.base / 'built', '3.0.0', self.arch, 0)
        shutil.copy2(archive, self.base / 'bundle.tar.gz')
        digest = hashlib.sha256((self.base / 'bundle.tar.gz').read_bytes()).hexdigest()
        (self.base / 'SHA256SUMS').write_text(digest + '  ' + self.name + '.tar.gz\n')
        destination = self.base / 'destination'
        destination.mkdir()
        self.put('tools/sh', '''#!/bin/sh
script=$1; shift
exec /bin/sh "$script" --root /opt/destination "$@"
''', 0o755)
        result = self.run_script('--language', 'en')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((destination / 'usr/bin/warpd').exists())
        config = destination / 'etc/config/warpd'
        account = destination / 'etc/warpd/warp-account.json'
        config.write_text('# existing custom config\n')
        account.write_text('PRIVATE TEST ACCOUNT\n')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(config.read_text(), '# existing custom config\n')
        self.assertEqual(account.read_text(), 'PRIVATE TEST ACCOUNT\n')
        self.clean()

    def test_version_must_be_single_line_even_if_one_line_is_valid_semver(self):
        result = self.run_script('--version', '3.0.0\nevil')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.base / 'events').exists())
        self.assertIn('semver', result.stderr)
        self.clean()

    def test_runtime_procd_helpers_are_part_of_required_packages(self):
        for tool in ('ubus', 'jsonfilter'):
            (self.root / 'tools' / tool).unlink()
        result = self.run_script('--check-only')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('ubus', result.stdout + result.stderr)
        self.assertIn('jsonfilter', result.stdout + result.stderr)

    def test_shared_filesystem_budgets_download_extraction_and_transaction_together(self):
        self.put('tools/df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 153600 0%% /\\n"\n', 0o755)
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('space', result.stderr.lower())
        self.assertFalse((self.base / 'events').exists())
        self.clean()

    def test_archive_metadata_must_match_requested_release_and_architecture(self):
        with tarfile.open(self.base / 'bundle.tar.gz', 'r:gz') as tar:
            contents = [(member, tar.extractfile(member).read() if member.isfile() else None) for member in tar]
        for field, value in (('ARCH', b'mips\n'), ('VERSION', b'2.1.1\n')):
            with tarfile.open(self.base / 'bundle.tar.gz', 'w:gz', format=tarfile.USTAR_FORMAT) as tar:
                for member, data in contents:
                    if member.name == self.name + '/' + field:
                        data = value
                        member.size = len(data)
                    tar.addfile(member, io.BytesIO(data) if data is not None else None)
            digest = hashlib.sha256((self.base / 'bundle.tar.gz').read_bytes()).hexdigest()
            (self.base / 'SHA256SUMS').write_text(digest + '  ' + self.name + '.tar.gz\n')
            result = self.run_script()
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse((self.base / 'invoked').exists())
            self.clean()

    def test_pinned_previous_release_archive_executes_real_inner_installer(self):
        archive = SCRIPT.parent / 'dist' / ('warpd-2.1.1-linux-' + self.arch + '.tar.gz')
        checksum = Path(str(archive) + '.sha256')
        if not archive.is_file() or not checksum.is_file():
            self.skipTest('cached published v2.1.1 archive/checksum unavailable')
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        self.assertEqual(checksum.read_text().split()[0], digest)
        shutil.copy2(archive, self.base / 'bundle.tar.gz')
        (self.base / 'SHA256SUMS').write_text(checksum.read_text())
        self.put('tools/curl', (self.root / 'tools/curl').read_text().replace('3.0.0', '2.1.1'), 0o755)
        (self.base / 'destination').mkdir()
        self.put('tools/sh', '#!/bin/sh\nscript=$1; shift\nexec /bin/sh "$script" --root /opt/destination "$@"\n', 0o755)
        result = self.run_script('--version', '2.1.1')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'destination/usr/share/warpd/VERSION').read_text(), '2.1.1\n')
        self.clean()

    def test_no_adjacent_service_or_network_commands_or_config_changes(self):
        for name in ('network', 'firewall', 'ssclash'):
            self.put('etc/config/' + name, 'OPERATOR ' + name + '\n')
        self.put('etc/warpd/warp-account.json', 'PRIVATE TEST ACCOUNT\n')
        self.put('etc/config/warpd', '# original config\n')
        for command in ('killall', 'pkill', 'service', 'nft', 'ip', 'iptables', 'ssclash'):
            self.put('tools/' + command, '#!/bin/sh\nprintf tripwire >> /opt/tripwire\nexit 98\n', 0o755)
        before = {p.relative_to(self.root / 'etc'): p.read_bytes() for p in (self.root / 'etc').rglob('*') if p.is_file()}
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        after = {p.relative_to(self.root / 'etc'): p.read_bytes() for p in (self.root / 'etc').rglob('*') if p.is_file()}
        self.assertEqual(after, before)
        self.assertFalse((self.base / 'tripwire').exists())
        self.clean()

    def test_explicit_reuse_fits_small_overlay_and_installs_no_unneeded_packages(self):
        self.put('tmp/shared', '#!/bin/sh\nexit 0\n', 0o755)
        self.put('tools/df', '''#!/bin/sh
case "$2" in /tmp) n=999999; mount=/tmp;; *) n=39936; mount=/overlay;; esac
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 %s 0%% %s\\n' "$n" "$mount"
''', 0o755)
        self.package_manager('opkg')
        result = self.run_script('--mihomo', '/tmp/shared', '--warpscout', '/tmp/shared', '--yes')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn('PKG', (self.base / 'events').read_text())
        self.assertEqual((self.root / 'tmp/shared').read_text(), '#!/bin/sh\nexit 0\n')
        self.assertEqual((self.base / 'invoked').read_text().splitlines(), ['--mihomo', '/tmp/shared', '--warpscout', '/tmp/shared'])

    def test_cleanup_failure_does_not_claim_success(self):
        self.put('tools/rm', '#!/bin/sh\ncase "$1" in -rf) exit 77;; esac\nexec /usr/bin/rm "$@"\n', 0o755)
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertNotIn('Installation complete', result.stdout)
        self.assertIn('Cannot clean', result.stderr)
        self.assertEqual((self.root / 'tmp/operator-sentinel').read_text(), 'keep me')

    def test_missing_package_manager_fails_honestly_without_attempted_downloads(self):
        self.missing_network_requirements()
        result = self.run_script('--yes')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('apk/opkg', result.stderr)
        self.assertFalse((self.base / 'events').exists())
        self.clean()

    def test_unavailable_file_size_limit_cannot_silently_run_uncapped_fetch(self):
        self.put('tools/source-shell', '#!/bin/sh\nulimit() { return 1; }\nscript=$1; shift\n. "$script"\n', 0o755)
        self.shell = ['/media/source-shell']
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.base / 'events').exists())
        self.clean()

    def test_private_and_configuration_dependencies_fail_before_download(self):
        for path in ('etc/warpd/profiles.json', 'etc/warpd/warp-account.json',
                     'usr/libexec/config.yaml', 'usr/libexec/configs/engine'):
            with self.subTest(path=path):
                self.put(path, '#!/bin/sh\n# SYNTHETIC PRIVATE FIXTURE\nexit 0\n', 0o755)
                before = (self.root / path).read_bytes()
                result = self.run_script('--mihomo', '/' + path)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse((self.base / 'events').exists(), 'private dependency reached download')
                self.assertEqual((self.root / path).read_bytes(), before)
                self.clean()

    def test_dependency_hardlink_cannot_alias_profile_credentials(self):
        self.put('etc/warpd/profiles.json', '#!/bin/sh\n# SYNTHETIC PRIVATE FIXTURE\nexit 0\n', 0o755)
        private = self.root / 'etc/warpd/profiles.json'
        alias = self.root / 'usr/libexec/shared-engine'
        os.link(private, alias)
        before = private.read_bytes()
        result = self.run_script('--warpscout', '/usr/libexec/shared-engine')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.base / 'events').exists(), 'credential hardlink reached download')
        self.assertEqual(private.read_bytes(), before)
        self.assertEqual(private.stat().st_mode & 0o777, 0o755)
        self.clean()

    def test_peak_space_includes_profile_rollback_copy(self):
        self.put('etc/warpd/profiles.json', 'x' * (2 * 1024 * 1024))
        self.put('tmp/shared', '#!/bin/sh\nexit 0\n', 0o755)
        available_kb = (1048576 + 12582912 + 1048576) // 1024
        self.put('tools/df', '#!/bin/sh\ncase "$2" in /tmp) n=999999; mount=/tmp;; *) n=' + str(available_kb) + '; mount=/overlay;; esac\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 %s 0%% %s\\n" "$n" "$mount"\n', 0o755)
        result = self.run_script('--mihomo', '/tmp/shared', '--warpscout', '/tmp/shared')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('space', result.stderr.lower())
        self.assertFalse((self.base / 'events').exists(), 'insufficient rollback space reached download')

    def test_check_only_reports_ready_without_download_or_install(self):
        result = self.run_script('--check-only')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('ready', result.stdout.lower())
        self.assertFalse((self.base / 'events').exists())
        self.assertFalse((self.base / 'invoked').exists())
        self.clean()


if __name__ == '__main__':
    unittest.main()
