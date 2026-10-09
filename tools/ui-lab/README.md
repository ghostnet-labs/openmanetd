# Radio UI lab

A local development lab for the two OpenMANET web interfaces (GHO-68). Both run on your machine:

| Interface | Address | Environment |
|---|---|---|
| OpenMANET dashboard | http://127.0.0.1:5173/ | Original React UI, live editing, simulated radio/mesh/GPS/battery data |
| OpenMANET LuCI | http://127.0.0.1:8083/cgi-bin/luci/ | OpenWrt 24.10.5 VM, project Argon theme, virtual Wi-Fi radios |

Use **Chrome for LuCI**. The in-app browser produced a LuCI startup error; Chrome rendered it correctly. LuCI login: **root**, empty password. All forwarded services listen only on this machine's loopback address.

The two interfaces use separate backends. Changing dashboard sample settings does not change LuCI's VM configuration. No physical radio is connected.

## Prerequisites

| Need | Where it comes from |
|---|---|
| Python 3 | System Python; the lab uses only the standard library |
| Node.js and pnpm | Any recent Node.js on `PATH` (or `UI_LAB_NODE`). Install the dashboard's dependencies once with `pnpm -C frontend install` from the openmanetd root |
| QEMU (`qemu-system-aarch64`) | `brew install qemu`, or set `UI_LAB_QEMU`. A copy unpacked into `$UI_LAB_WORK/runtime/qemu` is also found |
| OpenWrt 24.10.5 armsr/armv8 kernel | `openwrt-24.10.5-armsr-armv8-generic-kernel.bin` from the OpenWrt download page below, saved as `$UI_LAB_WORK/openwrt-kernel.bin`. Check it against that page's `sha256sums` |
| LuCI VM disk | `$UI_LAB_WORK/luci-rootfs.img`: the matching `generic-ext4-rootfs.img.gz`, unpacked, with LuCI installed, an empty root password, and SSH on the LAN address. **No script builds this disk yet**; the first copy was set up by hand. Without it, `up` starts only the dashboard and sample backend |
| `luci` and `packages` checkouts | `ghostnet-labs/luci` (branch `mm-23.05`) and `ghostnet-labs/packages` (branch `24.10`), cloned beside this openmanetd checkout |

Only the LuCI half needs QEMU and the VM disk. The dashboard and sample backend run with Python and Node alone.

### Paths

| Variable | Default |
|---|---|
| `UI_LAB_OPENMANETD` | The openmanetd checkout that contains this folder |
| `UI_LAB_LUCI` | `../luci` beside that checkout |
| `UI_LAB_PACKAGES` | `../packages` beside that checkout |
| `UI_LAB_WORK` | `~/.cache/openmanet-ui-lab`: VM kernel and disk, logs, `known_hosts`, process IDs |
| `UI_LAB_NODE` | `node` on `PATH` |
| `UI_LAB_QEMU` | `qemu-system-aarch64` on `PATH` |

Use your own branches in these checkouts. The lab reads and syncs whatever is checked out.

## Edit the interfaces

- Dashboard pages: `openmanetd/frontend/src/pages/`
- Dashboard shell: `openmanetd/frontend/src/Layout.jsx` and `Layout.css`
- Dashboard design tokens: `openmanetd/frontend/src/styles/lattice.css`
- LuCI OpenMANET theme: `packages/luci/luci-theme-openmanetargon/`
- LuCI standard pages: `luci/modules/luci-mod-status`, `luci-mod-network`, `luci-mod-system`
- Additional OpenMANET LuCI apps: `packages/luci/` (available to edit, not installed in this generic VM)
- Dashboard sample fixtures: `tools/sample-api.py`

Dashboard edits appear automatically. For LuCI, run the sync command below and refresh Chrome. Sync deploys theme files and the matching LuCI page resources, RPC handlers, menu definitions, and access rules. RPC restart ends existing LuCI sessions; log in again if prompted.

Follow each repository's instructions before changing product code. This setup does not introduce visual changes to either product.

## Start, stop, and sync

From this folder (`tools/ui-lab` in openmanetd):

```sh
python3 lab.py up
python3 lab.py status
python3 lab.py sync
python3 lab.py down
```

`up` starts missing services and leaves them running. `down` shuts down the VM and stops the dashboard/sample backend. VM configuration survives shutdown. Sample dashboard settings reset when its backend restarts.

Logs, the VM kernel and disk, and process IDs live in `UI_LAB_WORK`, outside the repository.

## What you can exercise

Dashboard: dashboard panels, topology, GPS display, wireless forms and save/reload, network views, talk-group controls, general hostname/configuration forms. Test radio data is clearly labeled as simulated.

LuCI: status, system and network configuration, the OpenMANET theme, and virtual wireless forms. The fork’s channel map is supplied from its checked-in setup test fixture for UI use. This is a generic OpenWrt 24.10.5 ARM VM, not an OpenMANET firmware build. The synced LuCI files come from the `mm-23.05` fork, which tracks OpenWrt 23.05, so check anything LuCI-specific on a real node before relying on it. HaLow hardware, Morse driver functions, range tests, and other custom apps need a compatible radio or a fuller firmware environment.

Real audio/PTT transmission, terminal access, firmware flashing, service restarts, setup application, speech-model download, and QR sharing/joining are not simulated by the dashboard backend. Unsupported actions return explicit errors. Neither UI validates RF behavior.

## Validation

- Both UIs rendered in a browser; LuCI also verified in Chrome.
- Dashboard radio settings saved through its actual UI to the sample backend.
- LuCI theme/resources synchronized successfully to the VM.
- OpenWrt kernel/rootfs downloads matched the official SHA-256 checksums.
- Focused sample-backend tests cover radio save/readback, radio isolation, and rejection of unsupported hardware actions.

```sh
python3 tools/test-sample-api.py
```

OpenWrt images: https://downloads.openwrt.org/releases/24.10.5/targets/armsr/armv8/
