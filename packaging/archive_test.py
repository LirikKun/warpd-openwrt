"""Archive-backed installer regressions; execute real POSIX/BusyBox scripts."""
import io
import os
from pathlib import Path
import shutil
import tarfile
import unittest
import installer_test as fixtures


class ArchiveTests(unittest.TestCase):
    base: Path
    root: Path
    bundle: Path
    setUp = fixtures.InstallerTests.setUp
    put = fixtures.InstallerTests.put
    checksums = fixtures.InstallerTests.checksums
    run_script = fixtures.InstallerTests.run_script
    install = fixtures.InstallerTests.install
    snapshot = fixtures.InstallerTests.snapshot
    live = fixtures.InstallerTests.live
    no_stat_environment = fixtures.InstallerTests.no_stat_environment
    def archive(self):
        archive = self.base / 'payload.tar.gz'
        with tarfile.open(archive, 'w:gz', format=tarfile.USTAR_FORMAT) as tar:
            tar.add(self.bundle, arcname='bundle')
        return archive

    def test_archive_live_failed_upgrade_restores_profile_snapshot_taken_after_stop(self):
        self.put(self.root / 'etc/warpd/profiles.json', 'BEFORE STOP PRIVATE TEST STORE', 0o640)
        config = "config warpd 'global'\n option enabled '1'\n option web_language 'ru'\n"
        self.put(self.root / 'etc/config/warpd', config, 0o640)
        self.put(self.base / 'fail-start', '2')
        result = self.live('''export TMPDIR=/opt
tar -czf /opt/payload.tar.gz -C /opt bundle
cp /opt/bundle/bin/warpd /usr/bin/warpd
printf old >> /usr/bin/warpd
cp /usr/bin/warpd /opt/old-binary
mkdir -p /etc/init.d
awk '{print; if ($0 ~ /echo stop >>/) print "  printf STOP-SAVED-TEST-STORE > /etc/warpd/profiles.json"}' /opt/bundle/files/warpd.init > /etc/init.d/warpd
chmod 755 /etc/init.d/warpd
/etc/init.d/warpd enable
/etc/init.d/warpd start
: > /opt/service-events
sh /opt/bundle/install.sh --archive /opt/payload.tar.gz --language en
status=$?
[ "$status" != 0 ] && /etc/init.d/warpd running
''', start_hook='''
  if [ "$n" = 2 ]; then printf NEW-FAILED-START-STORE > /etc/warpd/profiles.json; fi
''')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Service start failed', result.stderr)
        self.assertEqual((self.root / 'etc/warpd/profiles.json').read_bytes(), b'STOP-SAVED-TEST-STORE')
        self.assertEqual((self.root / 'etc/warpd/profiles.json').stat().st_mode & 0o777, 0o640)
        self.assertEqual((self.root / 'etc/config/warpd').read_bytes(), config.encode())
        self.assertEqual((self.root / 'usr/bin/warpd').read_bytes(), (self.base / 'old-binary').read_bytes())
        self.assertEqual((self.base / 'service-events').read_text().splitlines(), ['stop', 'start', 'stop', 'start'])
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))
        self.assertFalse(list(self.base.glob('warpd-archive.*')))

    def test_archive_live_failed_fresh_start_restores_profile_absence(self):
        self.put(self.base / 'fail-start', '1')
        result = self.live('''export TMPDIR=/opt
tar -czf /opt/payload.tar.gz -C /opt bundle
sh /opt/bundle/install.sh --archive /opt/payload.tar.gz
''', start_hook='''
  mkdir -p /etc/warpd
  printf CREATED-TEST-STORE > /etc/warpd/profiles.json
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Service start failed', result.stderr)
        self.assertFalse((self.root / 'etc/warpd/profiles.json').exists())
        self.assertFalse((self.root / 'etc/config/warpd').exists())
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))
        self.assertFalse(list(self.base.glob('warpd-archive.*')))

    def test_archive_truncated_gzip_footer_is_rejected_with_busybox(self):
        archive = self.archive()
        archive.write_bytes(archive.read_bytes()[:-8])
        env, tools, _ = self.no_stat_environment(busybox=True)
        busybox = os.environ['WARPD_TEST_BUSYBOX']
        (tools / 'tar').symlink_to(busybox)
        (tools / 'gzip').symlink_to(busybox)
        before = self.snapshot()
        result = self.run_script('install.sh', '--archive', str(archive), env=env, shell=[busybox, 'ash'])
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.snapshot(), before)

    def test_archive_validation_rejects_corruption_truncation_coverage_and_unsafe_entries(self):
        before = self.snapshot()
        archive = self.archive()
        original = archive.read_bytes()
        with tarfile.open(archive, 'r:gz') as tar:
            members = [(m, tar.extractfile(m).read() if m.isfile() else None) for m in tar]
        for mutation in ('corrupt-reused', 'checksum-duplicate', 'checksum-missing', 'checksum-unknown', 'duplicate', 'symlink', 'hardlink', 'fifo', 'traversal', 'extra', 'oversize-meta', 'oversize-payload', 'aggregate', 'truncated', 'compressed-cap'):
            with self.subTest(mutation=mutation):
                shared = self.base / 'shared-engine'
                self.put(shared, '#!/bin/sh\nexit 99\n', 0o751)
                with tarfile.open(archive, 'w:gz', format=tarfile.USTAR_FORMAT) as tar:
                    for old, old_data in members:
                        import copy
                        member, data = copy.copy(old), old_data
                        if mutation == 'corrupt-reused' and member.name == 'bundle/bin/mihomo':
                            data += b'corrupt'
                        if member.name == 'bundle/SHA256SUMS':
                            if mutation == 'checksum-duplicate': data += data.splitlines(keepends=True)[0]
                            if mutation == 'checksum-missing': data = b''.join(data.splitlines(keepends=True)[1:])
                            if mutation == 'checksum-unknown': data += b'0' * 64 + b'  unknown\n'
                        if mutation == 'oversize-meta' and member.name == 'bundle/ARCH': data = b'x' * 129
                        if mutation == 'oversize-payload' and member.name == 'bundle/README.md': data = b'x' * 1048577
                        if mutation == 'aggregate' and member.name in ('bundle/bin/mihomo', 'bundle/bin/warpd'):
                            data = b'x' * (50 * 1024 * 1024)
                        if data is not None: member.size = len(data)
                        tar.addfile(member, io.BytesIO(data) if data is not None else None)
                    if mutation in ('duplicate', 'symlink', 'hardlink', 'fifo', 'traversal', 'extra'):
                        member = tarfile.TarInfo({'duplicate': 'bundle/ARCH', 'traversal': '../escape'}.get(mutation, 'bundle/extra'))
                        member.type = {'symlink': tarfile.SYMTYPE, 'hardlink': tarfile.LNKTYPE, 'fifo': tarfile.FIFOTYPE}.get(mutation, tarfile.REGTYPE)
                        member.linkname = '/etc/passwd' if mutation in ('symlink', 'hardlink') else ''
                        tar.addfile(member)
                if mutation == 'truncated': archive.write_bytes(original[:len(original)//2])
                if mutation == 'compressed-cap':
                    with archive.open('wb') as f: f.truncate(41943041)
                result = self.run_script('install.sh', '--archive', str(archive), '--mihomo', str(shared))
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(self.snapshot(), before)
                self.assertFalse(list(self.base.glob('warpd-archive.*')))
                self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_archive_tar_producer_failure_is_not_hidden_by_valid_hash_or_staged_bytes(self):
        archive = self.archive()
        before = self.snapshot()
        wrappers = self.base / 'wrappers'
        real_tar = shutil.which('tar')
        marker = self.base / 'count'
        self.put(wrappers / 'tar', f'''#!/bin/sh
if [ "$1" = -xOzf ] && [ "$3" = bundle/bin/warpd ]; then
 n=0; [ ! -f "{marker}" ] || n=$(cat "{marker}")
 n=$((n+1)); printf '%s' "$n" > "{marker}"
 {real_tar} "$@" || exit
 [ "$n" != "$FAIL_AT" ] || exit 73
 exit 0
fi
exec {real_tar} "$@"
''', 0o755)
        for fail_at in (1, 2):
            with self.subTest(fail_at=fail_at):
                marker.unlink(missing_ok=True)
                result = self.run_script('install.sh', '--archive', str(archive), env=dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH'], FAIL_AT=str(fail_at)))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('Archive extraction failed', result.stderr)
                self.assertEqual(self.snapshot(), before)
                self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_full_archive_reuse_checks_skipped_dependencies_but_never_stages_them(self):
        archive = self.archive()
        shared = self.base / 'shared'
        self.put(shared, '#!/bin/sh\nexit 99\n', 0o751)
        wrappers = self.base / 'wrappers'
        log = self.base / 'tar.log'
        log.mkdir()
        self.put(wrappers / 'tar', f'#!/bin/sh\nrow=$(mktemp "{log}/row.XXXXXX")\nprintf "%s\\n" "$*" > "$row"\nexec {shutil.which("tar")} "$@"\n', 0o755)
        self.install('--archive', str(archive), '--mihomo', str(shared), '--warpscout', str(shared), env=dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH']))
        rows = [p.read_text().strip() for p in log.iterdir()]
        for name in ('mihomo', 'warpscout'):
            self.assertEqual(sum(row.endswith(' bundle/bin/' + name) for row in rows), 1)
        self.assertEqual(sum(row.endswith(' bundle/bin/warpd') for row in rows), 2)
        self.assertEqual(shared.read_text(), '#!/bin/sh\nexit 99\n')
        self.assertEqual(shared.stat().st_mode & 0o777, 0o751)

    def test_archive_commit_fault_restores_config_profile_and_old_files(self):
        self.install()
        self.put(self.root / 'etc/config/warpd', "config warpd 'global'\n option web_language 'ru'\n", 0o640)
        self.put(self.root / 'etc/warpd/profiles.json', 'PRIVATE TEST STORE', 0o640)
        self.put(self.root / 'usr/bin/warpd', 'OLD', 0o751)
        before = self.snapshot()
        wrappers = self.base / 'wrappers'
        marker = self.base / 'mv-count'
        self.put(wrappers / 'mv', f'''#!/bin/sh
n=0; [ ! -f "{marker}" ] || n=$(cat "{marker}")
n=$((n+1)); printf '%s' "$n" > "{marker}"
[ "$n" != 9 ] || exit 74
exec {shutil.which('mv')} "$@"
''', 0o755)
        result = self.run_script('install.sh', '--archive', str(self.archive()), '--language', 'en', env=dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH']))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.snapshot(), before)
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_archive_busybox_no_stat_lifecycle(self):
        env, tools, _ = self.no_stat_environment(busybox=True)
        busybox = os.environ['WARPD_TEST_BUSYBOX']
        shell = [busybox, 'ash']
        (tools / 'tar').symlink_to(busybox)
        archive = self.archive()
        self.install('--archive', str(archive), '--language', 'ru', env=env, shell=shell)
        self.install('--archive', str(archive), env=env, shell=shell)
        result = self.run_script('uninstall.sh', env=env, shell=shell)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / 'etc/config/warpd').exists())
        self.assertFalse(list(self.root.rglob('.warpd-stage.*')))

    def test_archive_metadata_requires_exact_canonical_lines(self):
        original = {name: (self.bundle / name).read_bytes() for name in ('ARCH', 'VERSION')}
        for name, data in [('ARCH', b'arm64\n\n'), ('VERSION', b'../invalid\n'), ('VERSION', b'3.1.1\n\n')]:
            with self.subTest(name=name, data=data):
                for key, value in original.items():
                    (self.bundle / key).write_bytes(value)
                (self.bundle / name).write_bytes(data)
                self.checksums()
                before = self.snapshot()
                result = self.run_script('install.sh', '--archive', str(self.archive()))
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('metadata', result.stderr)
                self.assertEqual(self.snapshot(), before)

    def test_transaction_space_diagnostic_reports_required_and_available(self):
        wrappers = self.base / 'wrappers'
        self.put(wrappers / 'df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 99999 0 1 0%% /\\n"\n', 0o755)
        result = self.run_script('install.sh', '--archive', str(self.archive()), env=dict(os.environ, PATH=str(wrappers) + ':' + os.environ['PATH']))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('transaction', result.stderr)
        self.assertIn('available 1024', result.stderr)
        self.assertIn('need ', result.stderr)

    def test_malformed_variants_and_archive_options_fail_without_mutation(self):
        before = self.snapshot()
        for data in ('full\n', 'core', 'core\n\n', 'unknown\n', 'no-MIHOMO\n', 'core\nno-mihomo\n'):
            with self.subTest(data=data):
                self.put(self.bundle / 'VARIANT', data)
                self.checksums()
                for mode in ([], ['--archive', str(self.archive())]):
                    result = self.run_script('install.sh', *mode)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('VARIANT', result.stderr)
                    self.assertEqual(self.snapshot(), before)
        for args in [('--archive',), ('--archive', 'relative'), ('--archive', '/missing'), ('--archive', '/missing', '--archive', '/missing')]:
            with self.subTest(args=args):
                result = self.run_script('install.sh', *args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.snapshot(), before)

    def test_slim_variants_require_explicit_reuse_in_archive_and_directory_modes(self):
        for variant, omitted in [('no-mihomo', ['mihomo']), ('no-warpscout', ['warpscout']), ('core', ['mihomo', 'warpscout'])]:
            with self.subTest(variant=variant):
                for name in ('mihomo', 'warpscout'):
                    self.put(self.bundle / 'bin' / name, '#!/bin/sh\nexit 0\n', 0o755)
                self.put(self.bundle / 'VARIANT', variant + '\n')
                flags = []
                for name in omitted:
                    (self.bundle / 'bin' / name).unlink()
                    external = self.base / ('shared-' + name)
                    self.put(external, '#!/bin/sh\nexit 91\n', 0o751)
                    flags += ['--' + name, str(external)]
                self.checksums()
                archive = self.archive()
                for mode in [[], ['--archive', str(archive)]]:
                    rejected = self.run_script('install.sh', *mode)
                    self.assertNotEqual(rejected.returncode, 0)
                    self.assertIn('requires explicit', rejected.stderr)
                    self.install(*mode, *flags)
                    for name in omitted:
                        self.assertEqual(os.readlink(self.root / ('usr/libexec/warpd/' + name)), str(self.base / ('shared-' + name)))
                    shutil.rmtree(self.root)
                    self.root.mkdir()
                for name in omitted:
                    self.put(self.bundle / 'bin' / name, '#!/bin/sh\nexit 0\n', 0o755)

    def test_archive_offline_root_inside_script_directory_preserves_existing_config(self):
        archive = self.archive()
        self.root = self.bundle / 'offline-root'
        self.root.mkdir()
        self.install('--archive', str(archive))
        config = self.root / 'etc/config/warpd'
        config.write_bytes(b'PRIVATE TEST CONFIG\x00\n')
        self.install('--archive', str(archive))
        self.assertEqual(config.read_bytes(), b'PRIVATE TEST CONFIG\x00\n')

    def test_archive_fresh_upgrade_directory_parity_without_payload_copy(self):
        self.install()
        expected = self.snapshot()
        shutil.rmtree(self.root)
        self.root.mkdir()
        archive = self.archive()
        shutil.rmtree(self.bundle / 'bin')
        shutil.rmtree(self.bundle / 'files')
        result = self.install('--archive', str(archive))
        self.assertEqual(self.snapshot(), expected)
        self.put(self.root / 'etc/config/warpd', 'PRIVATE TEST CONFIG\x00\n', 0o600)
        self.put(self.root / 'etc/warpd/profiles.json', 'PRIVATE TEST PROFILE\n', 0o600)
        before = self.snapshot()
        self.install('--archive', str(archive))
        self.assertEqual(self.snapshot(), before)
        self.assertFalse(list(self.base.rglob('.warpd-stage.*')))


if __name__ == '__main__':
    unittest.main()
