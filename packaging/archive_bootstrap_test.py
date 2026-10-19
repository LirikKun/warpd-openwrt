"""Low-memory bootstrap execution through real namespaces, tar, SHA and shells."""
import hashlib
import io
import os
from pathlib import Path
import tarfile
import unittest
import bootstrap_test as fixtures


class ArchiveBootstrapTests(unittest.TestCase):
    base: Path
    root: Path
    name: str
    arch: str
    setUp = fixtures.BootstrapTests.setUp
    put = fixtures.BootstrapTests.put
    bundle = fixtures.BootstrapTests.bundle
    run_script = fixtures.BootstrapTests.run_script
    clean = fixtures.BootstrapTests.clean
    def rewrite(self, script=None, variant=None, changes=None):
        with tarfile.open(self.base / 'bundle.tar.gz', 'r:gz') as tar:
            files = {m.name[len(self.name)+1:]: tar.extractfile(m).read() for m in tar if m.isfile()}
        if script is not None:
            files['install.sh'] = script.encode()
        if variant:
            self.name += '-' + variant
            files['VARIANT'] = (variant + '\n').encode()
            for dep in ('mihomo', 'warpscout'):
                if variant == 'core' or variant == 'no-' + dep:
                    files.pop('bin/' + dep)
        files.update(changes or {})
        files['SHA256SUMS'] = ''.join(hashlib.sha256(data).hexdigest() + '  ' + name + '\n' for name, data in sorted(files.items()) if name != 'SHA256SUMS').encode()
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
        digest = hashlib.sha256((self.base / 'bundle.tar.gz').read_bytes()).hexdigest()
        (self.base / 'SHA256SUMS').write_text(digest + '  ' + self.name + '.tar.gz\n')

    def test_archive_control_metadata_requires_exact_single_lines_before_invocation(self):
        original = (self.base / 'bundle.tar.gz').read_bytes()
        for field, value in [('ARCH', self.arch + '\n\n'), ('VERSION', '3.1.2\n\n')]:
            with self.subTest(field=field):
                (self.base / 'bundle.tar.gz').write_bytes(original)
                self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf invoked > /opt/invoked\n', changes={field: value.encode()})
                result = self.run_script()
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('metadata', result.stderr)
                self.assertFalse((self.base / 'invoked').exists())
                self.clean()

    def test_archive_split_shared_budget_boundaries_do_not_charge_download_twice(self):
        self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf invoked > /opt/invoked\n', changes={'bin/warpd': os.urandom(131072)})
        with tarfile.open(self.base / 'bundle.tar.gz') as tar:
            new = sum(m.size + 4096 for m in tar if m.isfile()) + 262144
        self.assertGreater((self.base / 'bundle.tar.gz').stat().st_size, 1024)
        for shared in (False, True):
            required = 1048576 + new + (1048576 if shared else 0)
            exact_kb = (required + 1023) // 1024
            for below in (True, False):
                with self.subTest(shared=shared, below=below):
                    (self.base / 'events').unlink(missing_ok=True)
                    (self.base / 'invoked').unlink(missing_ok=True)
                    self.put('tools/df', '#!/bin/sh\nn=999999; mount=/\nif [ -f /opt/events ]; then\n' +
                             (' n=' + str(exact_kb - int(below)) + '\n' if shared else
                              ' case "$2" in /tmp) n=1024; mount=/tmp;; *) n=' + str(exact_kb - int(below)) + '; mount=/overlay;; esac\n') +
                             'fi\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 %s 0%% %s\\n" "$n" "$mount"\n', 0o755)
                    result = self.run_script()
                    self.assertEqual(result.returncode == 0, not below, result.stdout + result.stderr)
                    self.assertEqual((self.base / 'invoked').exists(), not below)
                    if below:
                        self.assertIn('archive-backed transaction', result.stderr)
                        self.assertIn('available ', result.stderr)
                    self.clean()

    def test_archive_mode_busybox_pipe_language_uses_controlling_terminal(self):
        self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf "%s\\n" "$@" > /opt/invoked\n')
        self.shell = [os.environ['WARPD_TEST_BUSYBOX'], 'ash']
        result = self.run_script(tty_input='2\n', piped=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        args = (self.base / 'invoked').read_text().splitlines()
        self.assertEqual(args[0], '--archive')
        self.assertEqual(args[-2:], ['--language', 'ru'])
        self.clean()

    def test_archive_budget_includes_language_edit_output_beside_preserved_config(self):
        self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf invoked > /opt/invoked\n')
        self.put('etc/config/warpd', 'x' * (2 * 1024 * 1024))
        with tarfile.open(self.base / 'bundle.tar.gz') as tar:
            new = sum(m.size + 4096 for m in tar if m.isfile()) + 262144
        # Room for download controls + backup + staged config, not edit output.
        post_kb = (1048576 + new + 1048576 + 2 * (2 * 1024 * 1024 + 4096) + 1023) // 1024
        self.put('tools/df', '#!/bin/sh\nn=999999\n[ ! -f /opt/events ] || n=' + str(post_kb) + '\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 999999 0 %s 0%% /\\n" "$n"\n', 0o755)
        result = self.run_script('--language', 'en')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('archive-backed transaction', result.stderr)
        self.assertFalse((self.base / 'invoked').exists())
        self.clean()

    def test_cached_published_3_1_1_uses_legacy_full_extraction(self):
        self.bundle(version='3.1.1')
        self.put('tools/curl', (self.root / 'tools/curl').read_text().replace('3.1.2', '3.1.1'), 0o755)
        archive = Path(os.environ.get('WARPD_TEST_LEGACY_3_1_1', str(fixtures.SCRIPT.parent / 'dist' / ('warpd-3.1.1-linux-' + self.arch + '.tar.gz'))))
        self.assertTrue(archive.is_file(), 'set WARPD_TEST_LEGACY_3_1_1 to the cached published archive')
        sidecar = Path(str(archive) + '.sha256')
        self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), sidecar.read_text().split()[0])
        with tarfile.open(archive) as tar:
            script = tar.extractfile(self.name + '/install.sh')
            self.assertIsNotNone(script)
            self.assertNotIn(b'# warpd-archive-install-v1', script.read())
        import shutil
        shutil.copy2(archive, self.base / 'bundle.tar.gz')
        (self.base / 'SHA256SUMS').write_text(sidecar.read_text())
        (self.base / 'destination').mkdir()
        self.put('tools/sh', '''#!/bin/sh
script=$1; shift
for arg in "$@"; do [ "$arg" != --archive ] || exit 96; done
[ -f "$(dirname "$script")/bin/mihomo" ] || exit 97
exec /bin/sh "$script" --root /opt/destination "$@"
''', 0o755)
        result = self.run_script('--version', '3.1.1')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.base / 'destination/usr/share/warpd/VERSION').read_text(), '3.1.1\n')
        self.clean()

    def test_variant_assets_are_gated_by_numeric_version_and_explicit_reuse(self):
        for version, deps, variant in [
            ('3.1.1', ['mihomo', 'warpscout'], None),
            ('2.1.1', ['mihomo'], None),
            ('3.1.2', ['mihomo'], 'no-mihomo'),
            ('3.1.2', ['warpscout'], 'no-warpscout'),
            ('3.1.2', ['mihomo', 'warpscout'], 'core'),
            ('3.10.0', ['mihomo', 'warpscout'], 'core'),
            ('4.0.0', [], None),
        ]:
            with self.subTest(version=version, variant=variant):
                self.bundle(version=version)
                self.rewrite(script='#!/bin/sh\n# warpd-archive-install-v1\nprintf "%s\\n" "$@" > /opt/invoked\n', variant=variant)
                self.put('tools/curl', '''#!/bin/sh
out=
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; *) url=$1; shift;; esac; done
printf '%s\\n' "$url" >> /opt/events
case "$url" in
''' + f' */v{version}/{self.name}.tar.gz.sha256) cp /opt/SHA256SUMS "$out";;\n */v{version}/{self.name}.tar.gz) cp /opt/bundle.tar.gz "$out";;\n *) exit 93;;\nesac\n', 0o755)
                flags = ['--version', version]
                for dep in deps:
                    self.put('shared-' + dep, '#!/bin/sh\nexit 94\n', 0o751)
                    flags += ['--' + dep, '/opt/root/shared-' + dep]
                result = self.run_script(*flags)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn(self.name + '.tar.gz', (self.base / 'events').read_text())
                self.clean()

    def test_low_tmp_archive_mode_retains_archive_without_expanding_payload(self):
        self.rewrite(script='''#!/bin/sh
# warpd-archive-install-v1
printf '%s\\n' "$@" > /opt/invoked
[ "$1" = --archive ] && [ -f "$2" ] || exit 71
[ ! -e "$(dirname "$0")/bin/warpd" ] || exit 72
''')
        self.put('tools/df', '''#!/bin/sh
case "$2" in /tmp) free=46080; mount=/tmp;; *) free=2097152; mount=/overlay;; esac
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 2097152 0 %s 0%% %s\\n' "$free" "$mount"
''', 0o755)
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        args = (self.base / 'invoked').read_text().splitlines()
        self.assertEqual(args[0], '--archive')
        self.assertIn('/' + self.name + '.tar.gz', args[1])
        self.clean()


if __name__ == '__main__':
    unittest.main()
