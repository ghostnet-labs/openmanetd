import tarfile
from lab_paths import WORK as work,OPENMANETD,LUCI,PACKAGES
archive=work/'luci-overlay.tar.gz'
with tarfile.open(archive,'w:gz') as t:
 theme=PACKAGES/'luci/luci-theme-openmanetargon'
 t.add(theme/'htdocs',arcname='www')
 # Sample channel map needed by the fork even for non-HaLow wireless editors.
 t.add(OPENMANETD/'testfixtures/setup-wizard/channels.csv',arcname='www/halow-channels.csv')
 t.add(theme/'luasrc/view',arcname='usr/lib/lua/luci/view')
 # Deploy editable resources with matching RPC handlers and ACLs.
 t.add(LUCI/'modules/luci-base/htdocs/luci-static/resources',arcname='www/luci-static/resources')
 for app in ['luci-mod-status','luci-mod-network','luci-mod-system']:
  t.add(LUCI/'modules'/app/'htdocs/luci-static/resources',arcname='www/luci-static/resources')
 for app in ['luci-base','luci-mod-status','luci-mod-network','luci-mod-system']:
  shared=LUCI/'modules'/app/'root/usr/share'
  if shared.exists():t.add(shared,arcname='usr/share')
import subprocess
with archive.open('rb') as payload:
 subprocess.run(['ssh','-o','BatchMode=yes','-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+str(work/'known_hosts'),'-o','ConnectTimeout=5','-p','2223','root@127.0.0.1', 'tar -xzf - -C / && uci set luci.themes.openmanetargon=/luci-static/openmanetargon && uci set luci.main.mediaurlbase=/luci-static/openmanetargon && uci commit luci && /etc/init.d/rpcd restart && /etc/init.d/uhttpd restart && echo UI_SYNC_DONE'],stdin=payload,check=True,timeout=45)
