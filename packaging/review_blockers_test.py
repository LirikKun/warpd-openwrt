"""Desired-invariant regressions for independent-review L1-L4 (real scripts)."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import unittest
import archive_bootstrap_test as outer
import archive_test as inner

MIB = 1024 * 1024
AWK = str(Path(shutil.which('awk') or '/usr/bin/awk').resolve())


class CapsTests(unittest.TestCase):
    base: Path
    root: Path
    name: str
    arch: str
    setUp = outer.ArchiveBootstrapTests.setUp
    put = outer.ArchiveBootstrapTests.put
    bundle = outer.ArchiveBootstrapTests.bundle
    rewrite = outer.ArchiveBootstrapTests.rewrite
    run_script = outer.ArchiveBootstrapTests.run_script
    clean = outer.ArchiveBootstrapTests.clean

    def prepare(self, variant, version='3.1.2'):
        self.bundle(version=version)
        self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf invoked > /opt/invoked\n', variant=None if variant == 'full' else variant)
        flags = ['--version', version]
        for dep in ('mihomo', 'warpscout'):
            if variant in ('core', 'no-' + dep):
                self.put('shared-' + dep, '#!/bin/sh\nexit 97\n', 0o751)
                flags += ['--' + dep, '/opt/root/shared-' + dep]
        self.put('tools/curl', '''#!/bin/sh
out=
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; *) url=$1; shift;; esac; done
printf '%s\\n' "$url" >> /opt/events
case "$url" in
''' + f' */v{version}/{self.name}.tar.gz.sha256) cp /opt/SHA256SUMS "$out";;\n */v{version}/{self.name}.tar.gz) cp /opt/bundle.tar.gz "$out";;\n *) exit 93;;\nesac\n', 0o755)
        return flags

    def budgets(self, tmp_kb=999999, ram_kb=999999):
        self.put('tools/df', '#!/bin/sh\ncase "$2" in /tmp) n=' + str(tmp_kb) + '; m=/tmp;; *) n=999999; m=/overlay;; esac\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 %s 0%% %s\\n" "$n" "$m"\n', 0o755)
        self.put('tools/awk', '#!/bin/sh\nfor arg in "$@"; do [ "$arg" != /proc/meminfo ] || { printf "' + str(ram_kb) + '\\n"; exit 0; }; done\nexec ' + AWK + ' "$@"\n', 0o755)

    def reset_events(self):
        for name in ('events', 'invoked'):
            (self.base / name).unlink(missing_ok=True)

    def test_L1_each_variant_initial_tmp_and_ram_exact_boundaries(self):
        for variant, cap in [('full', 40), ('no-mihomo', 12), ('no-warpscout', 36), ('core', 8)]:
            flags = self.prepare(variant)
            for resource, kb in [('tmp', (cap + 1) * 1024), ('ram', (cap + 9) * 1024)]:
                for below in (True, False):
                    with self.subTest(variant=variant, resource=resource, below=below):
                        self.reset_events()
                        self.budgets(**{resource + '_kb': kb - int(below)})
                        result = self.run_script(*flags)
                        self.assertEqual(result.returncode == 0, not below, result.stdout + result.stderr)
                        self.assertEqual((self.base / 'invoked').exists(), not below)
                        if below:
                            self.assertIn('download preflight', result.stderr)
                            self.assertFalse((self.base / 'events').exists())
                        self.clean()

    def test_L1_core_actual_fixture_passes_10MiB_tmp_20MiB_ram(self):
        flags = self.prepare('core')
        self.budgets(tmp_kb=10240, ram_kb=20480)
        result = self.run_script(*flags)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.base / 'invoked').exists())
        self.clean()

    def test_L1_selected_download_cap_rejects_one_byte_excess(self):
        for variant, cap in [('full', 40), ('no-mihomo', 12), ('no-warpscout', 36), ('core', 8)]:
            with self.subTest(variant=variant):
                flags = self.prepare(variant)
                with (self.base / 'bundle.tar.gz').open('r+b') as f:
                    f.truncate(cap * MIB + 1)
                self.reset_events()
                result = self.run_script(*flags)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('HTTPS download failed', result.stderr)
                self.assertFalse((self.base / 'invoked').exists())
                self.clean()


class RamTests(unittest.TestCase):
    base: Path
    root: Path
    name: str
    setUp = outer.ArchiveBootstrapTests.setUp
    put = outer.ArchiveBootstrapTests.put
    bundle = outer.ArchiveBootstrapTests.bundle
    rewrite = outer.ArchiveBootstrapTests.rewrite
    run_script = outer.ArchiveBootstrapTests.run_script
    clean = outer.ArchiveBootstrapTests.clean

    def test_L2_nested_ram_backed_destination_uses_longest_proc_mount(self):
        self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf invoked > /opt/invoked\n', changes={'bin/warpd': b'x' * (16 * MIB)})
        self.put('tools/awk', f'''#!/bin/sh
for arg in "$@"; do
 if [ "$arg" = /proc/meminfo ]; then
  if [ -f /opt/events ]; then printf '9216\\n'; else printf '999999\\n'; fi
  exit 0
 fi
done
# Substitute only mount observations, leaving the production parser intact.
case "$*" in */proc/mounts*)
 set -- "$@"; count=$#
 while [ "$count" -gt 0 ]; do arg=$1; shift; [ "$arg" != /proc/mounts ] || arg=/opt/root/mounts; set -- "$@" "$arg"; count=$((count-1)); done;;
esac
exec {AWK} "$@"
''', 0o755)
        for kind, success in [('tmpfs', False), ('ramfs', False), ('ext4', True)]:
            with self.subTest(kind=kind):
                self.put('mounts', 'root / ext4 rw 0 0\nram /tmp tmpfs rw 0 0\nnested /usr/bin ' + kind + ' rw 0 0\n')
                for p in ('events', 'invoked'): (self.base / p).unlink(missing_ok=True)
                result = self.run_script()
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                self.assertEqual((self.base / 'invoked').exists(), success)
                self.clean()

    def test_L2_postdownload_ram_includes_transaction_growth_not_retained_archive(self):
        self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf invoked > /opt/invoked\n', changes={'bin/warpd': b'x' * (16 * MIB)})
        self.put('etc/config/warpd', 'OLD-CONFIG\n')
        self.put('etc/warpd/profiles.json', 'OLD-PROFILE\n')
        old = MIB + 2 * (len('OLD-CONFIG\n') + 4096) + len('OLD-PROFILE\n') + 4096
        with tarfile.open(self.base / 'bundle.tar.gz') as tar:
            new = sum(m.size + 4096 for m in tar if m.isfile()) + 262144
        exact_kb = (9 * MIB + old + new + 1023) // 1024
        for fstype, available, success in [('tmpfs', 9216, False), ('ramfs', exact_kb - 1, False), ('tmpfs', exact_kb, True), ('ext4', 9216, True)]:
            with self.subTest(fstype=fstype, ram_kb=available):
                for p in ('events', 'invoked'): (self.base / p).unlink(missing_ok=True)
                self.put('tools/df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 999999 0%% /\\n"\n', 0o755)
                self.put('tools/awk', f'''#!/bin/sh
for arg in "$@"; do
 if [ "$arg" = /proc/meminfo ]; then
  if [ -f /opt/events ]; then printf '{available}\\n'; else printf '999999\\n'; fi
  exit 0
 fi
 if [ "$arg" = /proc/mounts ]; then printf '{fstype}\\n'; exit 0; fi
done
exec {AWK} "$@"
''', 0o755)
                result = self.run_script()
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                self.assertEqual((self.base / 'invoked').exists(), success)
                if not success: self.assertIn('RAM during archive-backed transaction', result.stderr)
                self.clean()


class MetadataOuterTests(unittest.TestCase):
    base: Path
    root: Path
    name: str
    arch: str
    setUp = CapsTests.setUp
    put = CapsTests.put
    bundle = CapsTests.bundle
    rewrite = CapsTests.rewrite
    run_script = CapsTests.run_script
    clean = CapsTests.clean
    prepare = CapsTests.prepare

    def test_L3_outer_exact_metadata_bytes_before_either_installer_mode(self):
        for field in ('ARCH', 'VERSION', 'VARIANT'):
            for marker in (False, True):
                for mutation in ('nul-end', 'nul-inside', 'nul-after', 'extra-lf', 'crlf', 'no-lf'):
                    with self.subTest(field=field, archive_mode=marker, mutation=mutation):
                        flags = self.prepare('core' if field == 'VARIANT' else 'full')
                        original = {'ARCH': self.arch, 'VERSION': '3.1.2', 'VARIANT': 'core'}[field].encode() + b'\n'
                        data = {'nul-end': original[:-1]+b'\0', 'nul-inside': original[:1]+b'\0'+original[1:], 'nul-after': original+b'\0', 'extra-lf': original+b'\n', 'crlf': original[:-1]+b'\r\n', 'no-lf': original[:-1]}[mutation]
                        self.rewrite(script='#!/bin/sh\n' + ('# warpd-archive-install-v1\n' if marker else '') + 'printf invoked > /opt/invoked\n', changes={field: data})
                        for name in ('events', 'invoked'): (self.base / name).unlink(missing_ok=True)
                        result = self.run_script(*flags)
                        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertFalse((self.base / 'invoked').exists())
                        self.clean()


class InnerFixTests(unittest.TestCase):
    base: Path
    root: Path
    bundle: Path
    setUp = inner.ArchiveTests.setUp
    put = inner.ArchiveTests.put
    checksums = inner.ArchiveTests.checksums
    run_script = inner.ArchiveTests.run_script
    install = inner.ArchiveTests.install
    snapshot = inner.ArchiveTests.snapshot
    live = inner.ArchiveTests.live
    archive = inner.ArchiveTests.archive
    no_stat_environment = inner.ArchiveTests.no_stat_environment

    def test_L4_uppercase_and_mixed_hex_directory_archive_parity_and_wrong_hash(self):
        for spelling in ('upper', 'mixed'):
            for wrong in (False, True):
                self.checksums()
                rows = []
                for row in (self.bundle / 'SHA256SUMS').read_text().splitlines():
                    digest, name = row.split('  ', 1)
                    if wrong and name == 'bin/warpd': digest = ('1' if digest[0] == '0' else '0') + digest[1:]
                    digest = digest.upper() if spelling == 'upper' else ''.join(c.upper() if i % 2 else c for i, c in enumerate(digest))
                    rows.append(digest + '  ' + name + '\n')
                (self.bundle / 'SHA256SUMS').write_text(''.join(rows))
                expected = None
                for mode in ('directory', 'archive'):
                    with self.subTest(spelling=spelling, wrong=wrong, mode=mode):
                        shutil.rmtree(self.root); self.root.mkdir()
                        before = self.snapshot()
                        args = [] if mode == 'directory' else ['--archive', str(self.archive())]
                        result = self.run_script('install.sh', *args)
                        self.assertEqual(result.returncode == 0, not wrong, result.stdout + result.stderr)
                        if wrong: self.assertEqual(self.snapshot(), before)
                        elif expected is None: expected = self.snapshot()
                        else: self.assertEqual(self.snapshot(), expected)
                        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_L3_metadata_rejects_noncanonical_bytes_in_directory_and_archive(self):
        for field in ('ARCH', 'VERSION', 'VARIANT'):
            if field == 'VARIANT':
                self.put(self.bundle / 'VARIANT', 'core\n')
                for dep in ('mihomo', 'warpscout'):
                    (self.bundle / 'bin' / dep).unlink()
                    self.put(self.base / ('shared-' + dep), '#!/bin/sh\nexit 99\n', 0o751)
                flags = ['--mihomo', str(self.base / 'shared-mihomo'), '--warpscout', str(self.base / 'shared-warpscout')]
            else: flags = []
            original = (self.bundle / field).read_bytes()
            for mutation in (original[:-1]+b'\0', original[:1]+b'\0'+original[1:], original+b'\0', original+b'\n', original[:-1]+b'\r\n', original[:-1]):
                for mode in ('directory', 'archive'):
                    with self.subTest(field=field, mutation=mutation, mode=mode):
                        shutil.rmtree(self.root); self.root.mkdir()
                        (self.bundle / field).write_bytes(mutation); self.checksums()
                        before = self.snapshot()
                        args = [] if mode == 'directory' else ['--archive', str(self.archive())]
                        result = self.run_script('install.sh', *args, *flags)
                        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertEqual(self.snapshot(), before)
                        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))
            (self.bundle / field).write_bytes(original)

    def test_L2_direct_live_archive_ram_refusal_precedes_transaction_and_service(self):
        self.put(self.root / 'etc/config/warpd', "config warpd 'global'\n option enabled '0'\n", 0o600)
        self.put(self.base / 'awk-wrapper', '''#!/bin/sh
for arg in "$@"; do
 [ "$arg" != /proc/meminfo ] || { printf '9216\\n'; exit 0; }
 [ "$arg" != /proc/mounts ] || { printf 'tmpfs\\n'; exit 0; }
done
exec /mnt/mawk "$@"
''', 0o755)
        result = self.live('''cp /opt/awk-wrapper /media/awk
export TMPDIR=/opt
tar -czf /opt/payload.tar.gz -C /opt bundle
sh /opt/bundle/install.sh --archive /opt/payload.tar.gz
''')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Insufficient RAM during transaction', result.stderr)
        self.assertFalse((self.base / 'service-events').exists())
        self.assertFalse((self.root / 'usr/bin/warpd').exists())
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))
        import re
        match = re.search(r'need (\d+) bytes additional', result.stderr)
        self.assertIsNotNone(match, result.stderr)
        assert match is not None
        needed = int(match.group(1))
        exact_kb = (needed + 1023) // 1024
        for available_kb, success in ((exact_kb - 1, False), (exact_kb, True)):
            self.put(self.base / 'awk-wrapper', (self.base / 'awk-wrapper').read_text().replace("printf '9216", "printf '" + str(available_kb)), 0o755)
            result = self.live('''cp /opt/awk-wrapper /media/awk
export TMPDIR=/opt
tar -czf /opt/payload.tar.gz -C /opt bundle
sh /opt/bundle/install.sh --archive /opt/payload.tar.gz
''')
            self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
            # Restore only the adapter's observation, never production state.
            self.put(self.base / 'awk-wrapper', (self.base / 'awk-wrapper').read_text().replace("printf '" + str(available_kb), "printf '9216"), 0o755)

    def test_L2_language_comment_heavy_host_working_memory_below_process_reserve(self):
        config = self.root / 'etc/config/warpd'
        self.put(config, "config warpd 'global'\n" + '# x\n' * 300000, 0o600)
        input_bytes = config.stat().st_size
        rss = self.base / 'rss-kb'
        wrapper = self.base / 'wrappers/awk'
        self.put(wrapper, f'''#!/bin/sh
for arg in "$@"; do
 case "$arg" in */.warpd-stage.*/new) exec /usr/bin/time -f %M -o "{rss}" {AWK} "$@";; esac
done
exec {AWK} "$@"
''', 0o755)
        result = self.run_script('install.sh', '--archive', str(self.archive()), '--language', 'en', env=dict(os.environ, PATH=str(wrapper.parent)+':'+os.environ['PATH']))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        peak = int(rss.read_text()) * 1024
        print('HOST_LANGUAGE_AWK input_bytes=' + str(input_bytes) + ' peak_rss_bytes=' + str(peak), flush=True)
        self.assertLess(peak, 9 * MIB, 'Host measurement only; not a firmware/RSS guarantee')
        self.assertEqual(config.read_bytes(), b"config warpd 'global'\n    option web_language 'en'\n" + b'# x\n' * 300000)

    def test_L2_language_line_limit_refuses_only_explicit_edits_busybox_and_host(self):
        for busybox in (False, True):
            for length in (65536, 65537):
                with self.subTest(busybox=busybox, length=length):
                    shutil.rmtree(self.root); self.root.mkdir()
                    config = self.root / 'etc/config/warpd'
                    data = b"config warpd 'global'\r\n" + b'#' + b'x' * (length - 1)
                    config.parent.mkdir(parents=True); config.write_bytes(data); config.chmod(0o640)
                    kwargs = {}
                    if busybox:
                        env, tools, _ = self.no_stat_environment(busybox=True)
                        (tools / 'tar').unlink(missing_ok=True)
                        (tools / 'tar').symlink_to(os.environ['WARPD_TEST_BUSYBOX'])
                        kwargs = {'env': env, 'shell': [os.environ['WARPD_TEST_BUSYBOX'], 'ash']}
                    archive = self.archive()
                    before = self.snapshot()
                    result = self.run_script('install.sh', '--archive', str(archive), '--language', 'en', **kwargs)
                    self.assertEqual(result.returncode == 0, length == 65536, result.stdout + result.stderr)
                    if length > 65536:
                        self.assertEqual(self.snapshot(), before)
                        self.assertIn('65536-byte line limit', result.stderr)
                        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))
                    self.install('--archive', str(archive), **kwargs)
                    if length > 65536: self.assertEqual(config.read_bytes(), data)
