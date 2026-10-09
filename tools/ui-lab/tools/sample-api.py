"""Loopback-only sample backend for the unmodified OpenMANET React UI."""
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
import json,time,copy
HOST='UI-LAB-SIMULATED'
macs=['02:00:00:00:00:01','02:00:00:00:00:02','02:00:00:00:00:03']
radios=[dict(name='radio0',displayName='HaLow mesh (sample)',hardwareName='Morse Micro MM6108 — simulated',band=4,interfaceName='wlan0'),dict(name='radio1',displayName='2.4 GHz access point (sample)',hardwareName='Sample Wi-Fi',band=1,interfaceName='wlan1')]
settings={r['name']:dict(ssid='OpenMANET-Lab',meshId='openmanet-lab',password='sample-only',channel='42' if i==0 else '6',bandwidth=15 if i==0 else 2,txPower=20,country='US',encryption=1 if i==0 else 2,disabled=False,mode=2 if i==0 else 1,meshRssiThreshold=-85) for i,r in enumerate(radios)}
interfaces=[dict(name=n,type=t,ipAddress=ip,macAddress=macs[0],status=1,rxBytes='25000000',txBytes='14000000',mtu=1500) for n,t,ip in [('br-lan',1,'10.41.1.1'),('eth0',2,'192.168.1.100'),('wlan0',4,''),('wlan1',3,''),('bat0',5,'')]]
gnss=dict(settings=dict(enableGps=True,source=1),outputProtocols=dict(sendAsNmea=True,sendAsCot=False,cotUid=HOST))
comms=dict(commsEnabled=True,controlSource=3,callsign=HOST)
blos=dict(blosEnabled=False,message='Sample sandbox: VPN disabled')
selected=1
config=dict(comms=dict(enable=True,controlSource="web",debug=False),frontend=dict(httpAddress="127.0.0.1:5173"))

def rpc(method,b):
 global selected
 now=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())
 if method=='GetSetupStatus':return dict(isEnabled=False,isSetupComplete=True,hasHalowRadio=True,alreadyConfigured=True,currentHostname=HOST,currentCountry='US',currentTimezone='America/Denver')
 if method=='GetDashboardStatus':return dict(deviceInfo=dict(hostname=HOST,model='OpenMANET UI sandbox — sample data',firmware='UI development',kernel='Simulated',architecture='sample'),systemResources=dict(uptime='86400s',localTime=now,cpuLoadPercent=14,memoryTotalBytes='536870912',memoryUsedBytes='161061273',overlayTotalBytes='1073741824',overlayUsedBytes='209715200',cpuTempCelsius=42),battery=dict(present=True,voltageVolts=7.6,currentAmps=0.9,powerWatts=6.84,chargePercent=78,cellCount=2),networkSummary=dict(entries=[dict(interfaceName=i['name'],displayName=i['name'],state=1,detail=i['ipAddress'],rxBytes=i['rxBytes'],txBytes=i['txBytes']) for i in interfaces]),activeServices=[dict(name=n,status=1,pid=100+i) for i,n in enumerate(['openmanetd','mesh11sd','gpsd'])])
 if method=='GetMeshSnapshot':return dict(status=dict(isConnected=True,connectedNeighbors=2,activeMeshInterfaces=1,isMeshGateway=True),nodes=[dict(hostname=n,ipaddr='10.41.1.'+str(i+1)) for i,n in enumerate([HOST,'SAMPLE-ALPHA','SAMPLE-BRAVO'])],neighbors=[dict(neighbor=n,hardwareAddress=macs[i+1],signal=-58-i*12,throughput=12-i*3,interface='wlan0') for i,n in enumerate(['SAMPLE-ALPHA','SAMPLE-BRAVO'])],interfaces=[dict(name='wlan0',interfaceType='mesh',frequency=915,channelWidth=2)])
 if method=='GetMeshTopology':return dict(topology=dict(selfMac=macs[0],selfHostname=HOST,algorithm='BATMAN_V',collectedAt=now,gossipCoverage=dict(published=3,total=3),nodes=[dict(mac=m,hostname=n,segment='local',hopsFromSelf=0 if i==0 else 1,isSelf=i==0,isGateway=i==0,myHardIfname='wlan0',gossipAgeSeconds=2) for i,(m,n) in enumerate(zip(macs,[HOST,'SAMPLE-ALPHA','SAMPLE-BRAVO']))],edges=[dict(fromMac=macs[0],toMac=m,metric=12,onMyPath=True) for m in macs[1:]]))
 if method=='GetMeshTopologyDelta':return dict(actualWindow='60s',reconverge='0.2s',routesAdded=0,routesLost=0,gatewayChanges=0)
 if method=='ListRadios':return dict(radios=radios)
 if method=='GetRadioSettings':
  r=b.get('radioName','radio0');return dict(settings=settings[r],availableChannels=['38','42','46','50'] if r=='radio0' else [str(i) for i in range(1,12)],availableBandwidths=[14,15,16,17] if r=='radio0' else [2,5],availableEncryptions=[1,2,5])
 if method=='GetRadioStatus':
  r=b.get('radioName','radio0');s=settings[r];return dict(status=dict(active=not s['disabled'],ssid=s['meshId'] if r=='radio0' else s['ssid'],mode='mesh' if r=='radio0' else 'ap',wifiMode=s['mode'],channel=int(s['channel']),frequency=915 if r=='radio0' else 2437,bandwidth='2 MHz' if r=='radio0' else '20 MHz',encryption='SAE',txPower=s['txPower'],connectedClients=1 if r=='radio1' else 0,meshPeers=2 if r=='radio0' else 0))
 if method=='UpdateRadioSettings':settings[b['radioName']]=copy.deepcopy(b['settings']);return dict(success=True,message='Saved to sample backend only')
 if method=='ListMeshPeers':return dict(peers=[dict(hostname='SAMPLE-ALPHA',macAddress=macs[1],signalDbm=-58,throughputMbps=12,lastSeen='2s')])
 if method=='ListConnectedClients':return dict(clients=[dict(hostname='Sample phone',macAddress='02:00:00:00:00:10',signalDbm=-48,rxRateBps='12000000',txRateBps='9000000',connected='1800s')])
 if method=='ListNetworkInterfaces':return dict(interfaces=interfaces)
 if method=='GetDHCPServerConfig':return dict(config=dict(interfaceName='br-lan',rangeStart='10.41.1.100',rangeEnd='10.41.1.200',leaseTime='12h',dnsForwardingEnabled=True,activeLeaseCount=1))
 if method in ['ListActiveDHCPLeases','ListStaticDHCPLeases']:return dict(leases=[dict(hostname='Sample phone',macAddress='02:00:00:00:00:10',ipAddress='10.41.1.101',expiresSeconds=3600)])
 if method=='GetGNSSStatus':return dict(position=dict(fixType=3,latitude=39.7392,longitude=-104.9903,altitude=1609,speed=0,heading=0,pdop=1.2,hdop=0.8,lastUpdate=now),satelliteStatus=dict(satellitesUsed=8,satellitesInView=12,lastUpdate=now,satellites=[dict(prn=i+1,elevation=20+i*5,azimuth=i*30,snr=30+i,used=i<8,constellation=1) for i in range(12)]))
 if method=='GetGNSSConfig':return gnss
 if method=='UpdateGNSSConfig':gnss.update(b);return dict(success=True,message='Saved to sample backend only')
 if method=='GetBLOSStatus':return blos
 if method=='ListBLOSPeers':return dict(peers=[])
 if method=='UpdateBLOSConfig':blos['blosEnabled']=b.get('enableBlos',False);return dict(success=True,message='Saved to sample backend only')
 if method=='GetCommsConfig':return comms
 if method=='UpdateCommsConfig':comms.update(commsEnabled=b.get('enableComms',True),controlSource=b.get('controlSource',3),callsign=b.get('callsign',HOST));return {}
 if method=='GetCommsStatus':return dict(activeTalkgroup=selected,availableTalkgroups=[1,2,3,4,5],callsign=comms['callsign'],codec='Opus (simulated)',ptimeMs=20,roundTripMs=12,talkgroupStates=[dict(talkgroup=i,address='239.1.1.'+str(i),port=5000+i,sendEnabled=i==selected,receiveEnabled=i==selected) for i in range(1,6)])
 if method=='SelectTalkGroup':selected=b['talkgroup'];return dict(success=True,message='Selected in sandbox')
 if method in ['SetSendTalkGroup','SetReceiveTalkGroup','SendPTTEvent']:return dict(success=True,message='Sample action; no audio transmission')
 if method=='GetAudioMixer':return dict(state=dict(available=False))
 raise NotImplementedError(method)

class Handler(BaseHTTPRequestHandler):
 def send(self,data,status=200):
  raw=json.dumps(data).encode();self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def do_GET(self):
  if self.path=='/api/settings/hostname':self.send(dict(hostname=HOST))
  elif self.path=='/api/settings/config':self.send(config)
  elif self.path=='/api/whisper/status':self.send(dict(downloaded=False,available=False))
  elif self.path=='/auth/check':self.send(dict(authenticated=True,username='ui-lab',authEnabled=False))
  elif self.path=='/health':self.send(dict(mode='simulated',hostname=HOST))
  else:self.send(dict(error='Hardware-only endpoint unavailable in sandbox'),404)
 def do_PUT(self):
  global HOST
  b=json.loads(self.rfile.read(int(self.headers.get("Content-Length",0))) or "{}")
  if self.path=="/api/settings/hostname":HOST=b["hostname"];self.send(dict(hostname=HOST))
  elif self.path=="/api/settings/config":config.update(b);self.send(config)
  else:self.send(dict(error="Not simulated"),501)
 def do_POST(self):
  try:
   b=json.loads(self.rfile.read(int(self.headers.get('Content-Length',0))) or '{}')
   if self.path.startswith('/auth/'):return self.send(dict(authenticated=True,username='ui-lab'))
   method=self.path.rsplit('/',1)[-1];self.send(rpc(method,b))
  except NotImplementedError:self.send(dict(code='unimplemented',message='Not simulated: '+self.path),501)
  except Exception as e:self.send(dict(code='invalid_argument',message=str(e)),400)
 def log_message(self,fmt,*args):
  if args and str(args[1]) not in ['200']:super().log_message(fmt,*args)
if __name__=='__main__':
 print('Sample API on http://127.0.0.1:8080 — all data is simulated',flush=True)
 ThreadingHTTPServer(('127.0.0.1',8080),Handler).serve_forever()
