package openmanet

import (
	"context"
	"time"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/util/board"
	"github.com/openmanet/openmanetd/internal/util/logger"
	"github.com/rs/zerolog"
)

// startHardwareManager builds the V1 hardware manager from config and runs
// its step loop until ctx is canceled. It returns nil when
// hardware.enable is false. On boards other than Ghostnet V1, or while
// hardware.actuationEnable is false (the default until the pin map is
// verified, GHO-9/GHO-21), the manager is telemetry-only and drives no line.
func startHardwareManager(ctx context.Context, cfg *config.Config) *hardware.Manager {
	hc := cfg.GetHardware()
	if !hc.Enable {
		return nil
	}

	log := logger.GetLogger("hardware")

	boardID := ""

	if b, err := board.NewBoardConfigInfo(); err != nil {
		log.Warn().Err(err).Msg("hardware: board.json unreadable; running telemetry-only")
	} else if b != nil {
		boardID = b.Model.ID
	}

	battery := system.NewHwmonBatteryProviderForBoard(boardID)

	mgr := hardware.New(hardware.Options{
		Log:   log,
		Lines: hardware.CdevLineOpener{},
		Bus: &hardware.SysfsBus{
			HaLowUSBDevice: hc.HaLowUSBDevice,
			HaLowNetdev:    hc.HaLowNetdev,
			WiFiPCIAddress: hc.WiFiPCIAddress,
			WiFiNetdev:     hc.WiFiNetdev,
		},
		Battery:               battery,
		Store:                 &hardware.FileStore{Path: hc.RecoveryStateFile},
		Watchdogs:             hardware.DevWatchdogOpener{},
		Shutdowner:            hardware.ExecShutdowner{},
		BoardID:               boardID,
		WatchdogIdentity:      hc.WatchdogIdentity,
		FaultInputs:           hc.FaultInputs,
		PackOhm:               hc.PackResistanceOhm,
		SeriesCells:           battery.SeriesCells(),
		ActuationEnable:       hc.ActuationEnable,
		WatchdogEnable:        hc.WatchdogEnable,
		BatteryShutdownEnable: hc.BatteryShutdownEnable,
	})

	go mgr.Run(ctx)

	return mgr
}

// hardwareStopTimeout bounds the shutdown wait so a wedged GPIO or sysfs
// call cannot hold up daemon exit.
const hardwareStopTimeout = 5 * time.Second

// hardwareAPI returns the manager as the handler interface, or a true nil
// interface (not a typed nil pointer) when the manager is disabled, so the
// handler reports FailedPrecondition instead of dereferencing nil.
func hardwareAPI(m *hardware.Manager) handlers.HardwareManager {
	if m == nil {
		return nil
	}

	return m
}

// waitHardwareManager waits, bounded, for the manager's step loop to exit
// after ctx is canceled: it releases its lines and closes the watchdog
// (cleanly, unless a host reset was requested).
func waitHardwareManager(m *hardware.Manager, log zerolog.Logger) {
	if m == nil {
		return
	}

	select {
	case <-m.Done():
	case <-time.After(hardwareStopTimeout):
		log.Warn().Dur("timeout", hardwareStopTimeout).Msg("Hardware manager did not stop in time")
	}
}
