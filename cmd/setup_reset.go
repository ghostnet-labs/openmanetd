/*
Copyright © 2026 OpenMANET

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/spf13/cobra"
)

// setupResetCmd unbricks a device whose setup wizard ran but ended
// up in an unreachable state. Flipping setup.complete back to false
// lets the wizard re-run from a console / serial / recovery shell,
// flipping auth.enable to false makes the wizard reachable without a
// session, and setting setup.enabled to true lifts the wizard's kill
// switch (which defaults to off, so a stock config.yml would otherwise
// keep the wizard hidden).
//
// This is the recovery path documented in
// docs/setup-wizard-recovery.md: when a device is reachable via
// console but not over the network after a wizard run, the operator
// runs `openmanetd setup-reset` to re-open the wizard for a second
// attempt.
var setupResetCmd = &cobra.Command{ //nolint:gochecknoglobals
	Use:   "setup-reset",
	Short: "Reset the setup wizard so it can be re-run",
	Long: `Reset the setup wizard's completion flag so the wizard can be re-run.

This is a recovery command for devices whose first wizard run left them in an
unreachable state (e.g. a reload failed and the device cannot be reached on
its new SSID/IP). It sets three flags in /etc/openmanetd/config.yml and one UCI flag:

  setup.enabled    = true    # lift the wizard's kill switch (off by default)
  setup.complete   = false   # the wizard becomes reachable again
  auth.enable      = false   # session auth is disabled so the wizard can run
                             #   without a login
  luci.wizard.used = 0       # both the LuCI and the Go wizard set this to 1
                             #   on completion and refuse to re-run while set

After running this command, restart openmanetd (typically via
'/etc/init.d/openmanetd restart') and reconnect to the wizard URL.

Use only via console/serial/recovery — running this on a working device
disables auth and reopens the wizard, which would reset all UCI state on
the next wizard run.`,
	Run: runSetupReset,
}

func init() {
	rootCmd.AddCommand(setupResetCmd)
}

// setupResetConfig is the slice of *config.Config that setup-reset
// needs. Defined at the consumer so tests can substitute a fake.
type setupResetConfig interface {
	PersistSetupReset() error
}

func runSetupReset(cmd *cobra.Command, _ []string) {
	cfg := config.New(nil)

	clearLuci := func() error {
		return network.ClearLuciWizardUsedWithReader(network.NewUCINetworkConfigReader()) //nolint:wrapcheck // resetSetup wraps it
	}

	if err := resetSetup(cfg, clearLuci, cmd.OutOrStdout()); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), err)
		os.Exit(1)
	}
}

// resetSetup reopens the wizard: it persists setup.enabled=true,
// setup.complete=false and auth.enable=false in one config.yml write
// (matching the atomicity of the wizard's PersistSetupAndAuth path),
// then clears luci.wizard.used through clearLuci. The config write
// happens first so a UCI failure still leaves config.yml reset and
// the error message says so.
func resetSetup(cfg setupResetConfig, clearLuci func() error, out io.Writer) error {
	if err := cfg.PersistSetupReset(); err != nil {
		return fmt.Errorf("setup-reset failed: %w", err)
	}

	if err := clearLuci(); err != nil {
		return fmt.Errorf("setup-reset: cleared config.yml flags but failed to clear luci.wizard.used: %w", err)
	}

	fmt.Fprintln(out,
		"setup-reset: setup.enabled=true, setup.complete=false, auth.enable=false, luci.wizard.used=0")
	fmt.Fprintln(out,
		"Restart openmanetd to reload the configuration:")
	fmt.Fprintln(out,
		"  /etc/init.d/openmanetd restart")

	return nil
}
