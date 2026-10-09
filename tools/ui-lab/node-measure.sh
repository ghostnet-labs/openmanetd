#!/bin/sh
# node-measure.sh — Resource measurements for openmanetd on a POC node (GHO-78)
#
# Runs ON the node (OpenWrt BusyBox ash; needs only /proc, wget, awk). Copy it
# over and run it as root:
#
#   scp -O tools/ui-lab/node-measure.sh root@<node>:/tmp/
#   ssh root@<node> sh /tmp/node-measure.sh all | tee node-measure-$(date +%F).txt
#
# Subcommands:
#   startup              restart openmanetd, report ms until :8080 serves the SPA
#                        and :8087 answers (service is restarted: radio links
#                        stay up, open browser sessions reconnect)
#   sample LABEL SECS    sample openmanetd RSS / peak RSS / threads / fds /
#                        CPU% every INTERVAL seconds for SECS, CSV to stdout,
#                        summary (avg/max) at the end
#   info                 firmware, board, binary size, config flags that matter
#   all                  info, startup, then `sample idle 120`; prompts for the
#                        browser phases (see docs/poc-node-measurements.md)
#
# Environment: INTERVAL (default 2), HTTP_PORT (8080), API_PORT (8087),
# SERVICE (openmanetd).
set -eu

INTERVAL=${INTERVAL:-2}
HTTP_PORT=${HTTP_PORT:-8080}
API_PORT=${API_PORT:-8087}
SERVICE=${SERVICE:-openmanetd}

die() { echo "node-measure: $*" >&2; exit 1; }

# uptime_cs prints the system uptime in centiseconds (BusyBox date has no %N).
uptime_cs() { awk '{ split($1, a, "."); print a[1] * 100 + substr(a[2] "00", 1, 2) }' /proc/uptime; }

pid_of() { pidof "$SERVICE" 2>/dev/null | awk '{ print $1 }'; }

http_ok() { wget -q -T 2 -O /dev/null "$1" 2>/dev/null; }

cmd_info() {
	echo "== info"
	grep -E '^(DISTRIB_DESCRIPTION|DISTRIB_REVISION)' /etc/openwrt_release 2>/dev/null || true
	echo "board: $(cat /tmp/sysinfo/model 2>/dev/null || echo unknown)"
	echo "cpus: $(grep -c ^processor /proc/cpuinfo)"
	awk '/MemTotal|MemAvailable/ { printf "%s %d MiB\n", $1, $2 / 1024 }' /proc/meminfo
	bin=$(command -v "$SERVICE" || true)
	[ -n "$bin" ] && echo "binary: $bin $(wc -c <"$bin") bytes"
	for key in auth luciProxy tlsHostPort memlimit instrumentation; do
		grep -n "$key" /etc/openmanetd/config.yml 2>/dev/null | sed 's/^/config: /' || true
	done
}

# listening reports whether something listens on TCP port $1 (any address).
listening() {
	hex=$(printf ':%04X' "$1")
	files=""
	for f in /proc/net/tcp /proc/net/tcp6; do [ -r "$f" ] && files="$files $f"; done
	# shellcheck disable=SC2086 # word splitting of the file list is intended
	awk -v p="$hex" '$2 ~ p "$" && $4 == "0A" { found = 1 } END { exit !found }' $files
}

report_ms() {
	if [ -n "$2" ]; then echo "$1=$(($2 * 10))"; else echo "$1=timeout(>60s)"; fi
}

cmd_startup() {
	echo "== startup"
	[ -x "/etc/init.d/$SERVICE" ] || die "/etc/init.d/$SERVICE not found"
	old=$(pid_of)
	t0=$(uptime_cs)
	"/etc/init.d/$SERVICE" restart >/dev/null 2>&1 || die "restart failed"
	proc="" spa="" api=""
	i=0
	while [ $i -lt 600 ]; do # 600 x ~100 ms = 60 s ceiling
		now=$(uptime_cs)
		pid=$(pid_of)
		# Only count a listener once the new process exists, so the old one
		# answering during shutdown cannot fake a fast start.
		if [ -n "$pid" ] && [ "$pid" != "$old" ]; then
			[ -z "$proc" ] && proc=$((now - t0))
			[ -z "$api" ] && listening "$API_PORT" && api=$((now - t0))
			[ -z "$spa" ] && http_ok "http://127.0.0.1:$HTTP_PORT/" && spa=$((now - t0))
			[ -n "$spa" ] && [ -n "$api" ] && break
		fi
		usleep 100000 2>/dev/null || sleep 1
		i=$((i + 1))
	done
	report_ms process_started_ms "$proc"
	report_ms api_listening_ms "$api"
	report_ms spa_served_ms "$spa"
	pid=$(pid_of)
	[ -n "$pid" ] && awk '/VmRSS|VmHWM|Threads/' "/proc/$pid/status"
	return 0
}

# proc_ticks prints utime+stime of the service, total CPU ticks.
proc_ticks() {
	awk '{ print $14 + $15 }' "/proc/$1/stat"
}
total_ticks() { awk '/^cpu / { t = 0; for (i = 2; i <= NF; i++) t += $i; print t }' /proc/stat; }

cmd_sample() {
	label=${1:-sample}
	secs=${2:-60}
	pid=$(pid_of)
	[ -n "$pid" ] || die "$SERVICE is not running"
	ncpu=$(grep -c ^processor /proc/cpuinfo)
	echo "== sample $label ${secs}s every ${INTERVAL}s (pid $pid, $ncpu cpus)"
	echo "label,t_s,rss_kib,hwm_kib,threads,fds,cpu_pct_of_one_core"
	p0=$(proc_ticks "$pid") c0=$(total_ticks)
	t=0
	while [ "$t" -lt "$secs" ]; do
		sleep "$INTERVAL"
		t=$((t + INTERVAL))
		[ -d "/proc/$pid" ] || die "$SERVICE (pid $pid) exited during sampling"
		p1=$(proc_ticks "$pid") c1=$(total_ticks)
		fds=$(ls "/proc/$pid/fd" | wc -l)
		awk -v l="$label" -v t="$t" -v fds="$fds" -v dp=$((p1 - p0)) -v dc=$((c1 - c0)) -v n="$ncpu" '
			/VmRSS/ { rss = $2 } /VmHWM/ { hwm = $2 } /Threads/ { th = $2 }
			END { cpu = dc > 0 ? 100 * dp * n / dc : 0; printf "%s,%d,%d,%d,%d,%d,%.1f\n", l, t, rss, hwm, th, fds, cpu }
		' "/proc/$pid/status"
		p0=$p1 c0=$c1
	done | tee /tmp/node-measure-$label.csv
	awk -F, -v l="$label" '
		{ n++; rss += $3; cpu += $7; if ($3 > mr) mr = $3; if ($7 > mc) mc = $7; if ($5 > mt) mt = $5; if ($6 > mf) mf = $6; hwm = $4 }
		END { if (n) printf "summary %s: rss avg %d KiB max %d KiB, peak (VmHWM) %d KiB, cpu avg %.1f%% max %.1f%%, threads max %d, fds max %d\n", l, rss / n, mr, hwm, cpu / n, mc, mt, mf }
	' /tmp/node-measure-$label.csv
}

pause() {
	printf '\n>> %s\n   Press Enter when ready... ' "$1"
	read -r _
}

cmd_all() {
	cmd_info
	cmd_startup
	echo "(settling 30 s after restart)"
	sleep 30
	cmd_sample idle 120
	pause "Open https://<node>:8081/ (Dashboard) in ONE browser tab and leave it in front."
	cmd_sample dashboard-1tab 120
	pause "Open the Dashboard in THREE more tabs/devices (four total), all in front."
	cmd_sample dashboard-4tabs 120
	pause "Open Comms in one tab, AUDIO on, hold PTT (or VOX) for most of the next 60 s."
	cmd_sample comms-ptt 60
	pause "Background every tab (switch apps / lock the phone)."
	cmd_sample background 120
	echo "== done; CSVs in /tmp/node-measure-*.csv"
}

case "${1:-}" in
	info) cmd_info ;;
	startup) cmd_startup ;;
	sample) shift; cmd_sample "$@" ;;
	all) cmd_all ;;
	*) sed -n '2,25p' "$0"; exit 2 ;;
esac
