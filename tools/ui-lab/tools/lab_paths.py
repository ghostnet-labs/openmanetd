"""Locations the lab uses. Each one can be overridden by the environment variable read beside it."""
import os,shutil
from pathlib import Path
LAB=Path(__file__).resolve().parents[1]
# Checkouts: this openmanetd repository, with luci and packages cloned beside it.
OPENMANETD=Path(os.environ.get('UI_LAB_OPENMANETD',LAB.parents[1]))
LUCI=Path(os.environ.get('UI_LAB_LUCI',OPENMANETD.parent/'luci'))
PACKAGES=Path(os.environ.get('UI_LAB_PACKAGES',OPENMANETD.parent/'packages'))
# Runtime state: VM kernel and disk, logs, known_hosts, process IDs. Keep it outside the repository.
WORK=Path(os.environ.get('UI_LAB_WORK',Path.home()/'.cache/openmanet-ui-lab'))
NODE=os.environ.get('UI_LAB_NODE') or shutil.which('node')

def qemu():
 """Return the QEMU binary and any library path it needs: UI_LAB_QEMU, then PATH, then a copy unpacked into WORK/runtime."""
 found=os.environ.get('UI_LAB_QEMU') or shutil.which('qemu-system-aarch64')
 if found:return found,''
 unpacked=next((WORK/'runtime/qemu').glob('*/bin/qemu-system-aarch64'),None)
 if not unpacked:raise SystemExit('qemu-system-aarch64 not found: run `brew install qemu` or set UI_LAB_QEMU')
 return str(unpacked),':'.join(str(p) for p in list((WORK/'runtime').glob('*/*/lib'))+list(Path('/opt/homebrew/opt').glob('*/lib')))
