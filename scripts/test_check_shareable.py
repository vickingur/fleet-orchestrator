"""Guard the upload boundary, including staged content that differs from disk."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('privacy', Path(__file__).with_name('check-shareable.py'))
privacy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(privacy)


class PrivacyTests(unittest.TestCase):
    def test_rejects_private_paths_keys_and_binary_content(self):
        cases = [
            ('notes.txt', b'/' + b'Users/' + b'person/private'),
            ('notes.txt', b'-----BEGIN ' + b'OPENSSH PRIVATE KEY-----'),
            ('.env', b'DEMO=value'),
            ('.git/config', b'[core]'),
            ('file.bin', b'\0binary'),
        ]
        for path, data in cases:
            with self.subTest(path=path):
                self.assertTrue(privacy.inspect(Path(path), data))

    def test_index_scan_reads_staged_blob_not_clean_working_copy(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            subprocess.run(['git', 'init', '-q', temp], check=True)
            file = root / 'notes.txt'
            file.write_bytes(b'-----BEGIN ' + b'OPENSSH PRIVATE KEY-----')
            subprocess.run(['git', 'add', 'notes.txt'], cwd=root, check=True)
            file.write_text('clean working copy\n')
            errors, count = privacy.scan(root, tracked=True, source_only=False)
            self.assertEqual(count, 1)
            self.assertTrue(any('private-key' in e for e in errors))

    def test_index_scan_rejects_even_fictional_working_inventory(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            subprocess.run(['git', 'init', '-q', temp], check=True)
            (root / 'fleet.toml').write_text('# fictional\n')
            subprocess.run(['git', 'add', 'fleet.toml'], cwd=root, check=True)
            errors, _ = privacy.scan(root, tracked=True, source_only=False)
            self.assertTrue(any('must not be tracked' in e for e in errors))

    def test_accepts_documentation_fixtures(self):
        self.assertEqual(privacy.inspect(Path('fixture.txt'), b'worker.example.invalid 192.0.2.10'), [])
