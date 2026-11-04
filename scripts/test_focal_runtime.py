"""Fixture-only safety checks for the Focal prerequisite builder."""
import importlib.util
import io
import os
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('focal_runtime', Path(__file__).with_name('focal-runtime.py'))
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class RuntimeSafety(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='zah-runtime-test-')
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def archive(self, entries):
        archive = self.root / 'test.tar'
        with tarfile.open(archive, 'w') as tar:
            for name, kind, data in entries:
                member = tarfile.TarInfo(name)
                if kind == 'link':
                    member.type = tarfile.SYMTYPE; member.linkname = data
                    tar.addfile(member)
                else:
                    raw = data.encode(); member.size = len(raw)
                    tar.addfile(member, io.BytesIO(raw))
        return archive

    def test_extract_regular_and_confined_link(self):
        source = self.archive([('src/a', 'file', 'ok'), ('src/b', 'link', 'a')])
        runtime.extract(source, self.root / 'out')
        self.assertEqual((self.root / 'out/b').read_text(), 'ok')

    def test_reject_parent_traversal(self):
        with self.assertRaises(ValueError):
            runtime.extract(self.archive([('src/../../outside', 'file', 'bad')]), self.root / 'out')
        self.assertFalse((self.root / 'outside').exists())

    def test_reject_absolute(self):
        with self.assertRaises(ValueError):
            runtime.extract(self.archive([('/outside', 'file', 'bad')]), self.root / 'out')

    def test_reject_link_escape(self):
        with self.assertRaises(ValueError):
            runtime.extract(self.archive([('src/link', 'link', '../outside')]), self.root / 'out')

    def test_reject_members_under_link(self):
        with self.assertRaises(ValueError):
            runtime.extract(self.archive([('src/a', 'link', 'b'), ('src/a/file', 'file', 'bad')]), self.root / 'out')

    def test_reject_duplicate(self):
        with self.assertRaises(ValueError):
            runtime.extract(self.archive([('src/a', 'file', 'one'), ('src/a', 'file', 'two')]), self.root / 'out')

    def prefix(self):
        prefix = self.root / 'runtime'; prefix.mkdir(mode=0o700)
        (prefix / 'example').write_text('not executable')
        (prefix / 'example').chmod(0o644)
        runtime.seal(prefix)
        return prefix

    def test_inventory_detects_changed_file(self):
        prefix = self.prefix()
        runtime.verify(prefix)
        (prefix / 'example').write_text('changed')
        with self.assertRaises(ValueError): runtime.verify(prefix)

    def test_inventory_detects_added_file(self):
        prefix = self.prefix()
        (prefix / 'added').write_text('changed')
        with self.assertRaises(ValueError): runtime.verify(prefix)

    def test_reject_group_writable(self):
        prefix = self.prefix(); (prefix / 'example').chmod(0o666)
        runtime.seal(prefix)
        with self.assertRaises(ValueError): runtime.verify(prefix)

    def test_reject_external_runtime_link(self):
        prefix = self.root / 'runtime'; prefix.mkdir(mode=0o700)
        (prefix / 'link').symlink_to('/bin/true')
        with self.assertRaises(ValueError): runtime.seal(prefix)

    def test_reject_moved_runtime(self):
        prefix = self.prefix(); target = self.root / 'moved'; prefix.rename(target)
        with self.assertRaises(ValueError): runtime.verify(target)

    def test_build_rejects_existing_directory(self):
        with self.assertRaises(FileExistsError): runtime.build(self.root, None)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_environment_preserves_proxy_without_loader_injection(self):
        before = dict(os.environ)
        try:
            os.environ['LD_PRELOAD'] = '/no-library'
            os.environ['https_proxy'] = 'fixture-value'
            result = runtime.base_env()
            self.assertNotIn('LD_PRELOAD', result)
            self.assertEqual(result['https_proxy'], 'fixture-value')
        finally:
            os.environ.clear(); os.environ.update(before)


if __name__ == '__main__':
    unittest.main()
