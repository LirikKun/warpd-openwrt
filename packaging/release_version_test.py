"""Keep the release daemon version synchronized with the online installer."""
import importlib.util
from pathlib import Path
import re
import unittest


class ReleaseVersionTests(unittest.TestCase):
    def test_current_daemon_and_online_default_are_3_1_2(self):
        repo = Path(__file__).resolve().parents[1]
        spec = importlib.util.spec_from_file_location('release_version_builder', repo / 'tools/build_release.py')
        assert spec is not None and spec.loader is not None
        builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(builder)
        self.assertEqual(builder.read_version(repo / 'main.go'), '3.1.2')
        defaults = re.findall(r'^VERSION=([0-9]+\.[0-9]+\.[0-9]+)$',
                              (repo / 'install-warpd.sh').read_text(), re.MULTILINE)
        self.assertEqual(defaults, ['3.1.2'])


if __name__ == '__main__':
    unittest.main()
