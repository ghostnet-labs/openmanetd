/*
Copyright © 2026 OpenMANET

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.
*/

// Command e2e-frontend runs the real frontend server (internal/frontend)
// for the browser end-to-end suite in frontend/e2e.
//
// It is the `openmanetd frontend` subcommand without the rest of the daemon:
// building the full binary needs the go-alfred bindings, miniaudio and the
// other hardware CGO dependencies, while this command only needs the
// frontend package. It serves a built SPA from a directory on disk instead
// of the go:embed copy, proxies /rpc, /auth and /api/sysupgrade to --api
// (the sample backend in tools/ui-lab), and, when --luci-upstream is set,
// enables frontend.luciProxy so /cgi-bin/, /luci-static/ and /ubus/ reach
// that upstream exactly as they reach uhttpd on a node.
//
// It is test tooling only; nothing in the firmware build references it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/frontend"
	"github.com/openmanet/openmanetd/internal/util/logger"
	"github.com/spf13/viper"
)

// options are the command-line settings of one harness run.
type options struct {
	static       string
	listen       string
	tlsListen    string
	api          string
	luciUpstream string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run starts the server and blocks until SIGINT/SIGTERM, returning the
// process exit code. Split from main so deferred cleanup runs before exit.
func run(args []string) int {
	opts, err := parseFlags(args, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		fmt.Fprintln(os.Stderr, "e2e-frontend:", err)

		return 2 //nolint:mnd // conventional usage-error exit code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := logger.InitLogging(ctx)
	cfg := newConfig(opts)

	log.Info().
		Str("static", opts.static).
		Str("listen", opts.listen).
		Str("tls", opts.tlsListen).
		Str("api", opts.api).
		Str("luci", opts.luciUpstream).
		Msg("starting e2e frontend server")

	srv := frontend.NewFrontendServer(ctx, cfg, os.DirFS(opts.static), nil, false, nil)
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error().Err(err).Msg("e2e frontend server failed")

		return 1
	}

	return 0
}

// parseFlags reads the harness flags. --static and --api are required.
func parseFlags(args []string, out io.Writer) (options, error) {
	var opts options

	fs := flag.NewFlagSet("e2e-frontend", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.static, "static", "", "directory holding the built SPA (index.html, assets/)")
	fs.StringVar(&opts.listen, "listen", "127.0.0.1:18081", "HTTP listen address")
	fs.StringVar(&opts.tlsListen, "tls-listen", "127.0.0.1:18443", "HTTPS listen address (self-signed certificate)")
	fs.StringVar(&opts.api, "api", "", "upstream API base URL, e.g. http://127.0.0.1:18087")
	fs.StringVar(&opts.luciUpstream, "luci-upstream", "", "LuCI upstream URL; empty leaves frontend.luciProxy disabled")

	if err := fs.Parse(args); err != nil {
		return options{}, fmt.Errorf("parse flags: %w", err)
	}

	if opts.static == "" {
		return options{}, errors.New("--static is required")
	}

	if opts.api == "" {
		return options{}, errors.New("--api is required")
	}

	if fi, err := os.Stat(opts.static); err != nil || !fi.IsDir() {
		return options{}, fmt.Errorf("--static %q is not a directory", opts.static)
	}

	return opts, nil
}

// newConfig maps the options onto the same config keys a node's
// /etc/openmanetd/config.yml would set.
func newConfig(opts options) *config.Config {
	v := viper.New()
	v.Set("openmanetFrontendHostPort", opts.listen)
	v.Set("frontend.tlsHostPort", opts.tlsListen)
	v.Set("openmanetAPIAddress", opts.api)
	v.Set("openmanetCommsAPIAddress", opts.api)

	if opts.luciUpstream != "" {
		v.Set("frontend.luciProxy.enable", true)
		v.Set("frontend.luciProxy.upstream", opts.luciUpstream)
	}

	return config.NewWithoutWatch(v)
}
