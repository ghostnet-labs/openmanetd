#!/bin/sh
# check-32bit.sh — compile for 32-bit targets (linux/mipsle and linux/arm).
#
# The firmware builds openmanetd for mipsle routers, where int is 32 bits,
# but CI otherwise only compiles for 64-bit hosts. A constant such as
# math.MaxUint32 that fits an int on amd64 overflows it on mipsle and
# breaks the firmware build long after the PR merged.
#
# Packages that use cgo (directly or through a dependency) need a cross C
# toolchain, so they are skipped; everything else is built and vetted,
# tests included, with CGO_ENABLED=0.
set -eu

cd "$(dirname "$0")/.."

cgo_pkgs="$(CGO_ENABLED=1 GOOS=linux GOARCH=mipsle go list -deps \
  -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' ./... | sort -u)"

pkgs="$(CGO_ENABLED=1 GOOS=linux GOARCH=mipsle go list \
  -f '{{.ImportPath}} {{join .Deps " "}}' ./... |
  awk -v cgo="$cgo_pkgs" '
    BEGIN { n = split(cgo, a, "\n"); for (i = 1; i <= n; i++) if (a[i] != "") c[a[i]] = 1 }
    {
      skip = ($1 in c)
      for (i = 2; i <= NF && !skip; i++) if ($i in c) skip = 1
      if (!skip) print $1
    }')"

if [ -z "$pkgs" ]; then
  echo "check-32bit: no pure-Go packages found" >&2
  exit 1
fi

for arch in mipsle arm; do
  echo "check-32bit: linux/$arch ($(echo "$pkgs" | wc -l | tr -d ' ') packages)"
  # shellcheck disable=SC2086 # word splitting is intended: one package per word
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build $pkgs
  # shellcheck disable=SC2086
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go vet $pkgs
done
