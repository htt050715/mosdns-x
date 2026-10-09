import importlib.util
import json
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

    def test_new_and_loopback_panels_use_lan(self):
        with patch.object(detect, 'lan_address', return_value='192.168.50.110'):
            self.assertEqual(detect.panel_address('plugins: []\n'), ('192.168.50.110', '9099'))
            self.assertEqual(detect.panel_address('api:\n  http: "127.0.0.1:9080"\n'), ('192.168.50.110', '9080'))
            self.assertEqual(detect.panel_address('api:\n  http: "0.0.0.0:9099"\n'), ('192.168.50.110', '9099'))

    def test_existing_lan_and_explicit_override(self):
        with patch.object(detect, 'lan_address', side_effect=AssertionError('should not autodetect')):
            self.assertEqual(detect.panel_address('api:\n  http: "192.168.50.110:9099"\n'), ('192.168.50.110', '9099'))
            os.environ['PANEL_IP'] = '127.0.0.1'
            self.assertEqual(detect.panel_address('plugins: []\n'), ('127.0.0.1', '9099'))

    def test_default_route_lan_ignores_docker(self):
        interfaces = [{'ifname': n, 'addr_info': [{'family': 'inet', 'scope': 'global', 'local': ip}]} for n,ip in [('docker0','172.17.0.1'), ('enp6s19','192.168.50.110'), ('eth1','10.0.0.2')]]
        with patch.object(detect, 'command', side_effect=[json.dumps(interfaces), json.dumps([{'dev':'enp6s19', 'prefsrc':'192.168.50.110'}])]):
            self.assertEqual(detect.lan_address(), '192.168.50.110')

    def test_multiple_addresses_require_override_without_default(self):
        interfaces = [{'ifname': n, 'addr_info': [{'family':'inet', 'scope':'global', 'local': ip}]} for n,ip in [('eth0','192.168.1.2'), ('eth1','10.0.0.2')]]
        with patch.object(detect, 'command', side_effect=[json.dumps(interfaces), '[]']):
            with self.assertRaisesRegex(ValueError, 'set PANEL_IP'):
                detect.lan_address()


if __name__ == '__main__':
    unittest.main()
