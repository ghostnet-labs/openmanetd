#!/usr/bin/env python3
"""Start, inspect, synchronize, or stop this local radio UI lab."""
import os,sys,json,subprocess,urllib.request,time,signal
from pathlib import Path
LAB=Path(__file__).resolve().parent
sys.path.insert(0,str(LAB/'tools'))
from lab_paths import WORK,NODE,OPENMANETD,qemu
STATE=WORK/'lab-processes.json'
URLS={'dashboard':'http://127.0.0.1:5173/','sample_api':'http://127.0.0.1:8080/health','luci':'http://127.0.0.1:8083/cgi-bin/luci/'}
def online(url):
 try:
  with urllib.request.urlopen(url,timeout=3) as r:return r.status<500
 except urllib.error.HTTPError as e:return e.code<500
 except Exception:return False

def up():
 if not NODE:raise SystemExit('node not found: install Node.js or set UI_LAB_NODE')
 WORK.mkdir(parents=True,exist_ok=True)
 state=json.loads(STATE.read_text()) if STATE.exists() else {}
 env=os.environ.copy();env['PATH']=str(Path(NODE).parent)+':'+env.get('PATH','');env['VITE_API_TARGET']='http://127.0.0.1:8080'
 def start(name,args,cwd=LAB,extra=None):
  log=(WORK/(name+'.log')).open('a');p=subprocess.Popen(args,cwd=cwd,env=extra or env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True);state[name]=p.pid;log.close()
 if not online(URLS['sample_api']):start('sample_api',[sys.executable,str(LAB/'tools/sample-api.py')])
 if not online(URLS['dashboard']):start('dashboard',[str(NODE),'node_modules/vite/bin/vite.js','--host','127.0.0.1','--port','5173','--strictPort'],OPENMANETD/'frontend')
 vm_ready=all((WORK/f).exists() for f in ['openwrt-kernel.bin','luci-rootfs.img'])
 if not vm_ready:print('LuCI VM skipped: put openwrt-kernel.bin and luci-rootfs.img in '+str(WORK)+' (see README.md)')
 if vm_ready and not online(URLS['luci']):
  binary,libs=qemu();vm_env=env.copy()
  if libs:vm_env['DYLD_LIBRARY_PATH']=libs
  start('luci',[binary,'-M','virt','-cpu','cortex-a72','-accel','tcg','-smp','2','-m','512','-display','none','-monitor','none','-serial','file:'+str(WORK/'luci-serial.log'),'-kernel',str(WORK/'openwrt-kernel.bin'),'-drive','file='+str(WORK/'luci-rootfs.img')+',format=raw,if=virtio','-append','root=/dev/vda rootwait console=ttyAMA0','-netdev','user,id=lan,net=192.168.1.0/24,dhcpstart=192.168.1.100,hostfwd=tcp:127.0.0.1:8083-192.168.1.1:80,hostfwd=tcp:127.0.0.1:2223-192.168.1.1:22','-device','virtio-net-pci,netdev=lan'],extra=vm_env)
 STATE.write_text(json.dumps(state,indent=2))
 deadline=time.monotonic()+45
 wanted=[u for n,u in URLS.items() if vm_ready or n!='luci']
 while time.monotonic()<deadline and not all(online(u) for u in wanted):time.sleep(1)
 status()

def status():
 for name,url in URLS.items():print(('RUNNING' if online(url) else 'STOPPED')+'  '+name+'  '+url)

def down():
 if online(URLS['luci']):subprocess.run(['ssh','-o','BatchMode=yes','-o','UserKnownHostsFile='+str(WORK/'known_hosts'),'-o','ConnectTimeout=3','-p','2223','root@127.0.0.1','poweroff'],timeout=10,check=False)
 state=json.loads(STATE.read_text()) if STATE.exists() else {}
 for name,pid in state.items():
  if name=='luci':continue
  try:os.kill(pid,signal.SIGTERM)
  except ProcessLookupError:pass
 STATE.write_text('{}')
 status()

if __name__=='__main__':
 action=sys.argv[1] if len(sys.argv)>1 else 'up'
 if action=='sync':subprocess.run([sys.executable,str(LAB/'tools/sync-luci.py')],check=True)
 elif action in ['up','down','status']:globals()[action]()
 else:raise SystemExit('Use: python3 lab.py up|down|status|sync')
