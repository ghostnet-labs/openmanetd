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
| OpenWrt 24.10.5 VM | `python3 lab.py build-vm` downloads the armsr/armv8 kernel and ext4 rootfs from the OpenWrt download page below, checks them against the release's `sha256sums`, boots the image once on its serial console, and installs `luci-compat`, `kmod-mac80211-hwsim` and `wpad-basic-mbedtls`. It needs internet access and takes a few minutes. LuCI, the empty root password and SSH come with the stock image. `--force` rebuilds an existing disk. Without the VM, `up` starts only the dashboard and sample backend |
| `luci` and `packages` checkouts | `ghostnet-labs/luci` (branch `mm-23.05`) and `ghostnet-labs/packages` (branch `24.10`), cloned beside this openmanetd checkout |

Only the LuCI half needs QEMU and the VM disk. The dashboard and sample backend run with Python and Node alone.

Node.js: use the major version in openmanetd's `.nvmrc` (currently 24). pnpm is pinned by `packageManager` in `frontend/package.json`; `corepack enable` picks up that exact version, or install pnpm from Homebrew.

### Set up on an Apple Silicon Mac (M-series)

This is the tested setup.

```sh
brew install qemu node@24 pnpm
export PATH="$(brew --prefix node@24)/bin:$PATH"   # node@24 is keg-only; or use nvm: nvm install && nvm use

git clone https://github.com/ghostnet-labs/openmanetd.git
git clone -b mm-23.05 https://github.com/ghostnet-labs/luci.git
git clone -b 24.10 https://github.com/ghostnet-labs/packages.git
cd openmanetd
pnpm -C frontend install
cd tools/ui-lab
python3 lab.py build-vm   # once: downloads OpenWrt, checks it, prepares the VM disk
python3 lab.py up
python3 lab.py sync       # deploy the luci and packages checkouts into the VM
```

The three checkouts must sit side by side (see Paths below). The VM is an ARM64 guest that QEMU currently runs with software emulation (`-accel tcg`) on every host, so a first boot takes a minute or two. Hardware acceleration (`hvf` on Apple Silicon) is a possible later speed-up and is not wired in yet.

### Set up on an Intel Mac

Use the same Homebrew packages and the same commands. An Intel CPU cannot accelerate an ARM64 guest, so the VM also runs under TCG emulation, a little slower than on Apple Silicon. Give `lab.py up` a couple of minutes before LuCI answers; `lab.py status` shows when it does.

### Set up on Linux

Install QEMU from your distribution (Debian/Ubuntu: `sudo apt-get install qemu-system-arm`, which provides `qemu-system-aarch64`), Node 24 (nvm or your package manager) and pnpm (`corepack enable`). Then run the same clone and `tools/ui-lab` commands. The VM runs under TCG emulation here too.

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
python3 lab.py build-vm   # once
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
- `build-vm` built a fresh disk from the release images. After `sync`, root login, the system and wireless pages, the OpenMANET theme and the channel map all loaded, and the simulated radio came up.
- Focused sample-backend tests cover radio save/readback, radio isolation, and rejection of unsupported hardware actions.

```sh
python3 tools/test-sample-api.py
```

OpenWrt images: https://downloads.openwrt.org/releases/24.10.5/targets/armsr/armv8/
