import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('detect', Path(__file__).resolve().parents[1] / 'detect-install.py')
detect = importlib.util.module_from_spec(spec)
spec.loader.exec_module(detect)


class ProcessDetectionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        (self.root / 'config.yaml').write_text('plugins: []\n')
        self.environment = patch.dict(os.environ, {}, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def test_existing_etc_style_install(self):
        root, config = detect.parse_process(['mosdns', 'start', '--as-service', '-d', str(self.root)], '/')
        self.assertEqual(root, str(self.root))
        self.assertEqual(config, str(self.root / 'config.yaml'))

    def test_explicit_custom_config_and_equal_flags(self):
        root, config = detect.parse_process(['mosdns', 'start', '--dir='+str(self.root), '--config=custom.yml'], '/')
        self.assertEqual((root, config), (str(self.root), str(self.root / 'custom.yml')))

    def test_relative_dir_uses_actual_process_cwd(self):
        root, config = detect.parse_process(['mosdns', 'start', '-d', 'dns', '-c', 'config.yaml'], str(self.root))
        self.assertEqual((root, config), (str(self.root), str(self.root / 'config.yaml')))

    def test_ambiguous_default_configs_require_override(self):
        (self.root / 'config.yml').write_text('plugins: []\n')
        with self.assertRaisesRegex(ValueError, 'uniquely detect'):
            detect.parse_process(['mosdns', 'start'], str(self.root))
        os.environ['MOSDNS_CONFIG'] = str(self.root / 'config.yml')
        self.assertEqual(detect.parse_process(['mosdns', 'start'], str(self.root))[1], os.environ['MOSDNS_CONFIG'])


if __name__ == '__main__':
    unittest.main()
