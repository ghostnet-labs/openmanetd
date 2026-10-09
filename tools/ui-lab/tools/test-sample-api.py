import unittest,importlib.util,copy
from pathlib import Path
spec=importlib.util.spec_from_file_location('sample',Path(__file__).with_name('sample-api.py'));api=importlib.util.module_from_spec(spec);spec.loader.exec_module(api)
class SampleBackendTests(unittest.TestCase):
 def test_radio_save_round_trip_and_isolation(self):
  original=copy.deepcopy(api.settings);updated=copy.deepcopy(original['radio0']);updated['meshId']='edited-mesh';updated['channel']='46'
  try:
   self.assertTrue(api.rpc('UpdateRadioSettings',dict(radioName='radio0',settings=updated))['success'])
   self.assertEqual(api.rpc('GetRadioSettings',dict(radioName='radio0'))['settings']['meshId'],'edited-mesh')
   self.assertEqual(api.rpc('GetRadioStatus',dict(radioName='radio0'))['status']['channel'],46)
   self.assertEqual(api.settings['radio1'],original['radio1'])
  finally:api.settings=original
 def test_hardware_actions_are_not_falsely_successful(self):
  for action in ['ExecuteQuickAction','ApplySetup','Sysupgrade','OpenTerminal']:
   with self.assertRaises(NotImplementedError):api.rpc(action,{})
 def test_simulated_data_is_identified(self):
  data=api.rpc('GetDashboardStatus',{});self.assertIn('sandbox',data['deviceInfo']['model']);self.assertIn('SIMULATED',data['deviceInfo']['hostname'])
if __name__=='__main__':unittest.main()
