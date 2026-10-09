#!/usr/bin/env python3
"""Download OpenWrt 24.10.5 and prepare the LuCI VM disk in UI_LAB_WORK.

Steps: fetch the armsr/armv8 kernel and ext4 rootfs, check them against the release's
sha256sums, boot the rootfs once on the serial console, give it outbound network access,
install LuCI's Lua compatibility layer and simulated Wi-Fi radios, then power it off.
LuCI itself, the empty root password and SSH come with the stock image.
"""
import gzip,hashlib,os,shutil,socket,subprocess,sys,tempfile,time,urllib.request
from pathlib import Path
from lab_paths import WORK,qemu
RELEASE='https://downloads.openwrt.org/releases/24.10.5/targets/armsr/armv8/'
KERNEL='openwrt-24.10.5-armsr-armv8-generic-kernel.bin'
ROOTFS='openwrt-24.10.5-armsr-armv8-generic-ext4-rootfs.img.gz'
DISK=WORK/'luci-rootfs.img'
# Unix socket paths are limited to about 100 bytes, so the console socket lives in a short temporary directory.
CONSOLE=Path(tempfile.mkdtemp(prefix='ui-lab-'))/'console.sock'
# QEMU user networking: the guest LAN is 192.168.1.0/24, QEMU's gateway is .2 and its DNS is .3.
SETUP=[
 "for i in $(seq 60); do ip -4 addr show br-lan 2>/dev/null | grep -q 192.168.1.1 && break; sleep 1; done; ip -4 addr show br-lan | grep -q 192.168.1.1",
 "uci set network.lan.gateway='192.168.1.2' && uci add_list network.lan.dns='192.168.1.3' && uci set system.@system[0].hostname='OpenMANET-LuCI-Lab' && uci commit",
 "ip route replace default via 192.168.1.2 && printf 'nameserver 192.168.1.3\\n' > /etc/resolv.conf",
 "opkg update",
 "opkg install luci-compat kmod-mac80211-hwsim wpad-basic-mbedtls",
 "rm -f /etc/config/wireless && wifi config && uci set wireless.default_radio0.ssid=OpenMANET-SIMULATED && uci set wireless.radio0.disabled=0 && uci commit wireless",
 "sync",
]

def download(name):
 target=WORK/name
 if not target.exists():
  print('Downloading '+name,flush=True)
  with urllib.request.urlopen(RELEASE+name,timeout=120) as r,open(str(target)+'.part','wb') as f:shutil.copyfileobj(r,f)
  os.replace(str(target)+'.part',target)
 return target

def verify(paths):
 with urllib.request.urlopen(RELEASE+'sha256sums',timeout=60) as r:sums={l.split()[1].lstrip('*'):l.split()[0] for l in r.read().decode().splitlines() if l.strip()}
 for p in paths:
  if hashlib.sha256(p.read_bytes()).hexdigest()!=sums[p.name]:raise SystemExit(p.name+' does not match the release sha256sums; delete it and retry')

class Console:
 def __init__(self):
  deadline=time.monotonic()+30
  while True:
   try:self.s=socket.socket(socket.AF_UNIX);self.s.connect(str(CONSOLE));break
   except OSError:
    if time.monotonic()>deadline:raise
    time.sleep(0.5)
  self.s.settimeout(1);self.buf=''
 def wait_for(self,text,timeout,poke=False):
  deadline=time.monotonic()+timeout
  while text not in self.buf:
   if time.monotonic()>deadline:raise SystemExit('Timed out waiting for the VM console; see the output above')
   if poke:self.s.sendall(b'\n')
   try:chunk=self.s.recv(65536).decode(errors='replace');print(chunk,end='',flush=True);self.buf+=chunk
   except TimeoutError:pass
  self.buf=self.buf[self.buf.index(text)+len(text):]
 def run(self,command,timeout=300):
  # The shell computes the marker, so the echoed command line never matches it.
  self.s.sendall((command+'; echo STEP_$((40+2))_$?\n').encode())
  self.wait_for('STEP_42_',timeout)
  deadline=time.monotonic()+5
  while '\n' not in self.buf and time.monotonic()<deadline:
   try:self.buf+=self.s.recv(65536).decode(errors='replace')
   except TimeoutError:pass
  status,_,self.buf=self.buf.partition('\n')
  return status.strip()

def main():
 force='--force' in sys.argv[1:]
 if DISK.exists() and not force:raise SystemExit(str(DISK)+' already exists; pass --force to rebuild it')
 WORK.mkdir(parents=True,exist_ok=True)
 kernel,packed=download(KERNEL),download(ROOTFS)
 verify([kernel,packed])
 shutil.copyfile(kernel,WORK/'openwrt-kernel.bin')
 with gzip.open(packed) as src,open(DISK,'wb') as dst:
  try:shutil.copyfileobj(src,dst)
  except gzip.BadGzipFile:pass  # OpenWrt pads the image after the gzip stream
 binary,libs=qemu();env=os.environ.copy()
 if libs:env['DYLD_LIBRARY_PATH']=libs
 vm=subprocess.Popen([binary,'-M','virt','-cpu','cortex-a72','-accel','tcg','-smp','2','-m','512','-display','none','-monitor','none','-serial','unix:'+str(CONSOLE)+',server=on,wait=off','-kernel',str(WORK/'openwrt-kernel.bin'),'-drive','file='+str(DISK)+',format=raw,if=virtio','-append','root=/dev/vda rootwait console=ttyAMA0','-netdev','user,id=lan,net=192.168.1.0/24,dhcpstart=192.168.1.100','-device','virtio-net-pci,netdev=lan'],env=env)
 try:
  console=Console()
  print('Waiting for OpenWrt to boot',flush=True)
  # The console also offers a shell during preinit (root@(none)); wait for the booted system's prompt.
  console.wait_for('root@OpenWrt',300,poke=True)
  for command in SETUP:
   status=console.run(command)
   if status!='0':raise SystemExit('VM setup step failed ('+status+'): '+command)
  console.s.sendall(b'poweroff\n')
  vm.wait(timeout=120)
 finally:
  if vm.poll() is None:vm.terminate();vm.wait(timeout=30)
  shutil.rmtree(CONSOLE.parent,ignore_errors=True)
 print('\nVM disk ready: '+str(DISK)+'\nNext: python3 lab.py up && python3 lab.py sync',flush=True)

if __name__=='__main__':main()
