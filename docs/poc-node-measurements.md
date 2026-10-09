# POC node measurements (GHO-78)

The browser suite in `frontend/e2e` covers routing, auth, setup handoff,
layout, accessibility, polling budgets and the PTT/WebSocket path against the
real frontend server and a simulated backend. What it cannot measure is cost
on the hardware: startup time, memory and CPU of `openmanetd`, how the node
copes with several open tabs, and PTT through a real radio. This page is the
run sheet for those, to be done once on the POC node with the current
firmware and once with the build under test, and filled in side by side.

None of this is a CI criterion. Record the numbers in the GHO-78 ticket.

## What you need

| Item | Notes |
|---|---|
| POC node | Current firmware image first, then the image with the build under test |
| Laptop on the mesh or the node's AP | openmanetd checkout, `pnpm -C frontend install` done |
| Android phone with Chrome | The field browser; the PTT checks need a real microphone |
| Optional: iPhone with Safari | Only to record differences; not a release gate |
| `tools/ui-lab/node-measure.sh` | Copied to the node (below); BusyBox `sh`, `/proc` and `wget` only |

Write down for each run: firmware revision (`cat /etc/openwrt_release`),
openmanetd commit, board, and whether `auth.enable`, `frontend.luciProxy.enable`
and TLS are on in `/etc/openmanetd/config.yml`.

## 1. Node resources (on the node)

```sh
scp -O tools/ui-lab/node-measure.sh root@<node>:/tmp/
ssh -t root@<node> sh /tmp/node-measure.sh all | tee node-measure-$(date +%F)-<image>.txt
```

`all` prints node info, restarts `openmanetd` and times it, then samples the
daemon every 2 s (RSS, peak RSS, threads, open fds, CPU % of one core) through
these phases, prompting before each:

| Phase | What to do when prompted | Duration |
|---|---|---|
| `idle` | Nothing open in any browser | 120 s |
| `dashboard-1tab` | Dashboard open in one tab, in front | 120 s |
| `dashboard-4tabs` | Dashboard open in four tabs or devices | 120 s |
| `comms-ptt` | Comms open, AUDIO on, hold PTT (or VOX) most of the time | 60 s |
| `background` | Every tab backgrounded (phone locked, other app in front) | 120 s |

Single subcommands: `startup`, `sample <label> <seconds>`, `info`. CSVs are
left in `/tmp/node-measure-*.csv` on the node.

Fill in:

| Metric | Current firmware | Build under test | Notes |
|---|---|---|---|
| `process_started_ms` / `api_listening_ms` / `spa_served_ms` | | | from `startup` |
| RSS avg / max, idle (KiB) | | | |
| RSS max, dashboard-4tabs (KiB) | | | |
| Peak RSS (VmHWM) over the whole run (KiB) | | | |
| CPU avg / max, idle (% of one core) | | | |
| CPU avg / max, dashboard-4tabs | | | |
| CPU avg / max, comms-ptt | | | |
| Threads max / fds max | | | a climb that never returns is a leak |
| `background` vs `idle` CPU | | | should match: hidden tabs stop polling |

Expectations to check against: `background` CPU is within noise of `idle`
(the e2e polling spec proves the browser stops polling when hidden; this
confirms the node sees it), fds and threads return to their idle level after
tabs close, and peak RSS stays under any `runtime.memlimit` set in config.

## 2. Polling and routing from a browser (laptop)

The e2e suite can drive a real node instead of the local fakes. Specs that
depend on the fakes (the LuCI echo check, the resolver-mapped insecure-origin
PTT check) skip themselves, and the settings write test only runs with
`E2E_ALLOW_WRITES=1`. On an auth-enabled node, save a logged-in session first:

```sh
cd frontend
# Only if auth.enable is on: log in once in the window that opens, then close it.
pnpm exec playwright codegen --ignore-https-errors --save-storage=node-session.json https://<node>:8081/

E2E_BASE_URL=https://<node>:8081 E2E_HTTPS_URL=https://<node>:8081 \
E2E_STORAGE_STATE=node-session.json \
  pnpm exec playwright test navigation polling ptt-tls layout a11y --project=desktop --project=mobile
```

(`PLAYWRIGHT_BROWSERS_PATH` or `E2E_CHROMIUM` as in the dev container if
Playwright's own Chromium is not installed.)

| Check | Result | Notes |
|---|---|---|
| Advanced entry → LuCI → back, through the node's real uhttpd | | `navigation.spec.js` |
| Deep links + reload on every SPA route, served by the node | | |
| Requests per minute within budget, and zero while hidden | | `polling.spec.js` prints the per-endpoint counts as annotations |
| PTT over https: secure context, `wss://<node>:8081/ws`, Opus frames sent | | `ptt-tls.spec.js` (fake mic) |
| Layout / touch targets / axe with real data | | real data can produce layouts the sample backend does not |

## 3. Bundle cost

On the build machine:

```sh
make frontend && make bundle-size        # gzip totals vs frontend/scripts/bundle-budget.json
```

Over the mesh, from a client one hop or more away (`--compressed` matches what
a browser negotiates; repeat three times and take the median):

```sh
for f in / $(curl -sk https://<node>:8081/ | grep -o '/assets/[^"]*' ); do
  curl -sk --compressed -o /dev/null -w "%{size_download} B  %{time_total} s  $f\n" "https://<node>:8081$f"
done
```

| Metric | Current firmware | Build under test |
|---|---|---|
| Initial JS + CSS transferred (bytes) | | |
| Time to load all initial assets over the mesh (s) | | |
| Time from tapping a bookmark to the dashboard showing data, Android Chrome (s) | | |

## 4. Browser PTT, TLS and WebSocket routing (Android Chrome, by hand)

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | Open `https://<node>:8081/`, accept the self-signed certificate | Dashboard loads; padlock shows "not secure" certificate warning only | |
| 2 | Comms page | `WS UP` chip; the socket is `wss://<node>:8081/ws` (chrome://inspect → Network → WS) | |
| 3 | Hold PTT the first time | Chrome asks for the microphone once; allow | |
| 4 | Hold PTT, speak, release | Peer radio hears audio; `TX start` / `TX end` in the Comms log; no `Mic unavailable` line | |
| 5 | Peer keys up | Audio plays with AUDIO on; RX waveform moves | |
| 6 | Open `http://<node>:8080/comms` (plain http, mesh IP) and hold PTT | Log says `Mic unavailable: page must be served over HTTPS…`; no audio sent. This is the browser's secure-context rule, not a product defect | |
| 7 | Advanced → LuCI → back to Comms over https, then PTT again | Still `WS UP`, PTT works; LuCI did not break the session | |
| 8 | Lock the phone 2 min, unlock | WebSocket reconnects by itself (`WS UP`) | |
| 9 | Reboot the node from the UI or LuCI with Comms open | Page shows `WS DOWN` during the reboot and recovers without a manual reload | |

## Classifying a failure

Record each failure with one of these, so the follow-up goes to the right place:

| Class | Typical signs | Goes to |
|---|---|---|
| Browser limitation | Only on plain http (secure-context APIs: microphone, `crossOriginIsolated`); only in one browser engine; the UI shows an explanatory message | Docs / operator guidance, not a code bug |
| Environment failure | Certificate not accepted, captive DNS, mesh link down (`ping` the node fails), the radio or audio device missing on this node, LuCI not installed | Node or network setup; rerun after fixing |
| Product defect | Reproduces on https in Chrome with a healthy mesh: wrong route, page error in the console, WebSocket refused by the daemon, audio not delivered while `WS UP`, numbers above budget | A ticket against openmanetd (or luci/packages for LuCI pages) with the step number and the node-measure output attached |
