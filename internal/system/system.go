package system

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

// Reboot initiates a system reboot by executing the "reboot" command.
func Reboot() error {
	cmd := exec.CommandContext(context.Background(), "reboot")

	return cmd.Run()
}

// addressServices are the init scripts reloaded when the node's mesh
// address changes: dnsmasq serves DHCP on it and the firewall zones
// reference its interface.
var addressServices = []string{"dnsmasq", "firewall"} //nolint:gochecknoglobals // fixed list, read-only

// selfRestartCmd restarts openmanetd from outside its own process tree.
// procd stops openmanetd during the restart, so the command runs in its
// own session and outlives the process that started it; the short delay
// lets the caller finish logging first.
const selfRestartCmd = "sleep 2; /etc/init.d/openmanetd restart"

// RestartForNewAddress brings the services that use the mesh address up
// on a new one after the network has been reloaded: it reloads dnsmasq
// and the firewall, then restarts openmanetd so every subsystem (comms,
// the ATAK feed, multicast relays) rebinds. It replaces the reboot that
// address reservation used to need.
func RestartForNewAddress(ctx context.Context) error {
	return restartForNewAddress(ctx, runInitScript, startDetached)
}

func restartForNewAddress(
	ctx context.Context,
	run func(ctx context.Context, name, action string) error,
	detach func(script string) error,
) error {
	for _, svc := range addressServices {
		if err := run(ctx, svc, "reload"); err != nil {
			return fmt.Errorf("reload %s: %w", svc, err)
		}
	}

	if err := detach(selfRestartCmd); err != nil {
		return fmt.Errorf("restart openmanetd: %w", err)
	}

	return nil
}

func runInitScript(ctx context.Context, name, action string) error {
	if err := exec.CommandContext(ctx, "/etc/init.d/"+name, action).Run(); err != nil {
		return fmt.Errorf("/etc/init.d/%s %s: %w", name, action, err)
	}

	return nil
}

// startDetached runs a shell script in a new session without waiting for
// it. It deliberately uses a background context: the script must survive
// this process being stopped.
func startDetached(script string) error {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %q: %w", script, err)
	}

	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release %q: %w", script, err)
	}

	return nil
}
