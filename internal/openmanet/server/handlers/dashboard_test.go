package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/openmanet/openmanetd/internal/api/openmanet/dashboard/v1"
	dashboardconnect "github.com/openmanet/openmanetd/internal/api/openmanet/dashboard/v1/dashboardv1connect"
	batmanadv "github.com/openmanet/openmanetd/internal/batman-adv"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/util/board"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// --- Mock implementations ---

type mockBoardProvider struct {
	board *board.Board
	err   error
}

func (m *mockBoardProvider) GetBoard() (*board.Board, error) {
	return m.board, m.err
}

type mockSysInfoProvider struct {
	hostname string
	kernel   string
	arch     string
	uptime   time.Duration
	memTotal int64
	memAvail int64
	cpuLoad  float32
	cpuTemp  float32
	overlay  system.OverlayUsage
	err      error
}

func (m *mockSysInfoProvider) GetHostname() (string, error) {
	if m.err != nil {
		return "", m.err
	}

	return m.hostname, nil
}

func (m *mockSysInfoProvider) GetKernelVersion() (string, error) {
	if m.err != nil {
		return "", m.err
	}

	return m.kernel, nil
}

func (m *mockSysInfoProvider) GetArchitecture() (string, error) {
	if m.err != nil {
		return "", m.err
	}

	return m.arch, nil
}

func (m *mockSysInfoProvider) GetUptime() (time.Duration, error) {
	if m.err != nil {
		return 0, m.err
	}

	return m.uptime, nil
}

func (m *mockSysInfoProvider) GetMemoryInfo() (*system.MemoryInfo, error) {
	if m.err != nil {
		return nil, m.err
	}

	return &system.MemoryInfo{TotalBytes: m.memTotal, AvailableBytes: m.memAvail}, nil
}

func (m *mockSysInfoProvider) GetCPULoadPercent() (float32, error) {
	if m.err != nil {
		return 0, m.err
	}

	return m.cpuLoad, nil
}

func (m *mockSysInfoProvider) GetCPUTempCelsius() (float32, error) {
	if m.err != nil {
		return 0, m.err
	}

	return m.cpuTemp, nil
}

func (m *mockSysInfoProvider) GetOverlayUsage() (*system.OverlayUsage, error) {
	if m.err != nil {
		return nil, m.err
	}

	return &m.overlay, nil
}

type mockFirmwareProvider struct {
	info *system.FirmwareInfo
	err  error
}

func (m *mockFirmwareProvider) GetFirmwareInfo() (*system.FirmwareInfo, error) {
	return m.info, m.err
}

type mockInterfaceProvider struct {
	ifaces []network.NetworkInterfaceInfo
	err    error
}

func (m *mockInterfaceProvider) ListAll() ([]network.NetworkInterfaceInfo, error) {
	return m.ifaces, m.err
}

type mockWifiProvider struct {
	count int
	err   error
}

func (m *mockWifiProvider) GetMeshNeighborCount() (int, error) {
	return m.count, m.err
}

type mockOriginatorProvider struct {
	originators []batmanadv.Originator
	err         error
}

func (m *mockOriginatorProvider) GetOriginators() ([]batmanadv.Originator, error) {
	return m.originators, m.err
}

type mockTailscaleProvider struct {
	running bool
}

func (m *mockTailscaleProvider) IsRunning() bool {
	return m.running
}

type mockServiceChecker struct {
	statuses []system.ServiceStatus
	err      error
}

func (m *mockServiceChecker) CheckServices(_ context.Context, _ []string) ([]system.ServiceStatus, error) {
	return m.statuses, m.err
}

type mockQuickActionExecutor struct {
	rebootErr    error
	networkErr   error
	openmanetErr error
}

func (m *mockQuickActionExecutor) Reboot(_ context.Context) error            { return m.rebootErr }
func (m *mockQuickActionExecutor) RestartNetwork(_ context.Context) error    { return m.networkErr }
func (m *mockQuickActionExecutor) RestartOpenmanetd(_ context.Context) error { return m.openmanetErr }

type mockBatteryProvider struct {
	mu     sync.Mutex // protects calls
	status *system.BatteryStatus
	err    error
	calls  int
}

func (m *mockBatteryProvider) GetBatteryStatus() (*system.BatteryStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls++

	return m.status, m.err
}

func (m *mockBatteryProvider) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.calls
}

// fakeHardwareStatus implements HardwareStatusReader.
type fakeHardwareStatus struct {
	mu     sync.Mutex // protects the fields below
	status hardware.Status
	calls  int
}

func (f *fakeHardwareStatus) Status() hardware.Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	return f.status
}

func (f *fakeHardwareStatus) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// fakeMonitorReader implements hardware.MonitorReader with a fixed reading.
type fakeMonitorReader struct {
	mu      sync.Mutex // protects reading
	reading system.MonitorReading
}

func (f *fakeMonitorReader) ReadMonitor(bool) (system.MonitorReading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.reading, nil
}

// makeHardwareBattery returns a manager status whose battery block holds
// the given reading.
func makeHardwareBattery(b hardware.BatteryStatus) *fakeHardwareStatus {
	return &fakeHardwareStatus{status: hardware.Status{Battery: b}}
}

// --- Tests ---

func newTestDashboardService() *DashboardService {
	return &DashboardService{
		Log: zerolog.Nop(),
		Board: &mockBoardProvider{
			board: &board.Board{
				Model: board.Model{Name: "MorseMicro HaLowLink 2"},
			},
		},
		SysInfo: &mockSysInfoProvider{
			hostname: "HaLowLink2-6cff",
			kernel:   "5.15.150",
			arch:     "mipsel_24kc",
			memTotal: 248168 * 1024,
			memAvail: 80000 * 1024,
			cpuLoad:  12.0,
			cpuTemp:  61.5,
			overlay:  system.OverlayUsage{TotalBytes: 3700000, UsedBytes: 3100000},
		},
		Firmware: &mockFirmwareProvider{
			info: &system.FirmwareInfo{Description: "OpenWrt 23.05.3 / OpenMANET 1.7.0"},
		},
		Interfaces: &mockInterfaceProvider{
			ifaces: []network.NetworkInterfaceInfo{
				{Name: "eth0", LinkType: network.LinkTypeEthernet, State: network.OperStateDown, RxBytes: 1024, TxBytes: 2048},
				{Name: "br-ahwlan", LinkType: network.LinkTypeBridge, State: network.OperStateUp, IP: "10.41.25.72/16", RxBytes: 142300000, TxBytes: 87600000},
				{Name: "phy1-ap0", LinkType: network.LinkTypeHaLowMesh, State: network.OperStateUp, RxBytes: 12345678, TxBytes: 9876543},
				{Name: "bat0", LinkType: network.LinkTypeBatman, State: network.OperStateUp, RxBytes: 5_000_000_000, TxBytes: 3_000_000_000},
				{Name: "tailscale0", LinkType: network.LinkTypeUnknown, State: network.OperStateDown, RxBytes: 555, TxBytes: 666},
			},
		},
		Wifi: &mockWifiProvider{count: 3},
		Originators: &mockOriginatorProvider{
			originators: []batmanadv.Originator{
				{OrigAddress: "aa:bb:cc:dd:ee:01"},
				{OrigAddress: "aa:bb:cc:dd:ee:02"},
				{OrigAddress: "aa:bb:cc:dd:ee:03"},
				{OrigAddress: "aa:bb:cc:dd:ee:04"},
			},
		},
		Tailscale: &mockTailscaleProvider{running: false},
		Services: &mockServiceChecker{
			statuses: []system.ServiceStatus{
				{Name: "openmanetd", State: system.ServiceStateRunning, PID: 1842},
				{Name: "openmanet-webui", State: system.ServiceStateRunning, PID: 2105},
				{Name: "dnsmasq", State: system.ServiceStateRunning, PID: 987},
				{Name: "hostapd", State: system.ServiceStateRunning, PID: 1102},
				{Name: "gpsd", State: system.ServiceStateRunning, PID: 1456},
				{Name: "batadv-vis", State: system.ServiceStateStopped, PID: 0},
			},
		},
		Actions: &mockQuickActionExecutor{},
	}
}

func TestDashboardService_GetDashboardStatus_DeviceInfo(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	di := resp.DeviceInfo
	require.NotNil(t, di)
	assert.Equal(t, "HaLowLink2-6cff", di.Hostname)
	assert.Equal(t, "MorseMicro HaLowLink 2", di.Model)
	assert.Equal(t, "OpenWrt 23.05.3 / OpenMANET 1.7.0", di.Firmware)
	assert.Equal(t, "5.15.150", di.Kernel)
	assert.Equal(t, "mipsel_24kc", di.Architecture)
}

func TestDashboardService_GetDashboardStatus_SystemResources(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	sr := resp.SystemResources
	require.NotNil(t, sr)
	assert.NotNil(t, sr.LocalTime)
	assert.InDelta(t, 12.0, float64(sr.CpuLoadPercent), 0.1)
	assert.InDelta(t, 61.5, float64(sr.CpuTempCelsius), 0.01)
	assert.Equal(t, int64(248168*1024), sr.MemoryTotalBytes)
	assert.Equal(t, int64((248168-80000)*1024), sr.MemoryUsedBytes)
	assert.Equal(t, int64(3700000), sr.OverlayTotalBytes)
	assert.Equal(t, int64(3100000), sr.OverlayUsedBytes)
}

func TestDashboardService_GetDashboardStatus_Battery(t *testing.T) {
	tests := []struct {
		name    string
		battery system.BatteryProvider
		want    *v1.BatteryStatus
	}{
		{
			name: "present",
			battery: &mockBatteryProvider{status: &system.BatteryStatus{
				Present: true, VoltageVolts: 11.4, CurrentAmps: -0.85, PowerWatts: 9.69,
				ChargePercent: 60, CellCount: 3,
			}},
			want: &v1.BatteryStatus{
				Present: true, VoltageVolts: 11.4, CurrentAmps: -0.85, PowerWatts: 9.69,
				ChargePercent: 60, CellCount: 3,
			},
		},
		{
			name:    "no monitor",
			battery: &mockBatteryProvider{status: &system.BatteryStatus{ChargePercent: -1}},
			want:    &v1.BatteryStatus{ChargePercent: -1},
		},
		{
			name:    "read error reports absent",
			battery: &mockBatteryProvider{err: errors.New("i2c read failed")},
			want:    &v1.BatteryStatus{ChargePercent: -1},
		},
		{
			name:    "no provider",
			battery: nil,
			want:    &v1.BatteryStatus{ChargePercent: -1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestDashboardService()
			svc.Battery = tt.battery

			resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
			require.NoError(t, err)
			require.NotNil(t, resp.Battery)
			assert.Equal(t, tt.want.Present, resp.Battery.Present)
			assert.InDelta(t, tt.want.VoltageVolts, resp.Battery.VoltageVolts, 0.001)
			assert.InDelta(t, tt.want.CurrentAmps, resp.Battery.CurrentAmps, 0.001)
			assert.InDelta(t, tt.want.PowerWatts, resp.Battery.PowerWatts, 0.001)
			assert.InDelta(t, tt.want.ChargePercent, resp.Battery.ChargePercent, 0.001)
			assert.Equal(t, tt.want.CellCount, resp.Battery.CellCount)
		})
	}
}

func TestDashboardService_GetDashboardStatus_HardwareBattery(t *testing.T) {
	monitor := &system.BatteryStatus{
		Present: true, VoltageVolts: 7.6, CurrentAmps: 0.2, PowerWatts: 1.5,
		ChargePercent: 55, CellCount: 2,
	}
	v1Reading := hardware.BatteryStatus{
		Present: true, BusMV: 11400, CurrentMA: 850, PowerMW: 9690,
		SeriesCells: 3, Level: hardware.BatteryLevelNormal, AbsentReason: hardware.AbsentReasonNone,
	}
	stale := v1Reading
	stale.Stale = true

	unknownPack := v1Reading
	unknownPack.SeriesCells = 0

	tests := []struct {
		name         string
		hw           *fakeHardwareStatus
		battery      *mockBatteryProvider
		want         *v1.BatteryStatus
		monitorReads int
	}{
		{
			name:    "V1 pack: manager telemetry, charge unknown, monitor not read",
			hw:      makeHardwareBattery(v1Reading),
			battery: &mockBatteryProvider{status: monitor},
			want: &v1.BatteryStatus{
				Present: true, VoltageVolts: 11.4, CurrentAmps: 0.85, PowerWatts: 9.69,
				ChargePercent: -1, CellCount: 3,
			},
		},
		{
			name:    "stale reading keeps last-good values",
			hw:      makeHardwareBattery(stale),
			battery: &mockBatteryProvider{status: monitor},
			want: &v1.BatteryStatus{
				Present: true, VoltageVolts: 11.4, CurrentAmps: 0.85, PowerWatts: 9.69,
				ChargePercent: -1, CellCount: 3,
			},
		},
		{
			name:    "unknown pack takes the charge estimate from the monitor",
			hw:      makeHardwareBattery(unknownPack),
			battery: &mockBatteryProvider{status: monitor},
			want: &v1.BatteryStatus{
				Present: true, VoltageVolts: 11.4, CurrentAmps: 0.85, PowerWatts: 9.69,
				ChargePercent: 55, CellCount: 2,
			},
			monitorReads: 1,
		},
		{
			name:    "unknown pack and monitor read failure leaves charge unknown",
			hw:      makeHardwareBattery(unknownPack),
			battery: &mockBatteryProvider{err: errors.New("i2c read failed")},
			want: &v1.BatteryStatus{
				Present: true, VoltageVolts: 11.4, CurrentAmps: 0.85, PowerWatts: 9.69,
				ChargePercent: -1, CellCount: 0,
			},
			monitorReads: 1,
		},
		{
			name: "manager has no reading yet: falls back to the monitor",
			hw: makeHardwareBattery(hardware.BatteryStatus{
				AbsentReason: hardware.AbsentReasonNone, Level: hardware.BatteryLevelUnknown,
			}),
			battery:      &mockBatteryProvider{status: monitor},
			want:         &v1.BatteryStatus{Present: true, VoltageVolts: 7.6, CurrentAmps: 0.2, PowerWatts: 1.5, ChargePercent: 55, CellCount: 2},
			monitorReads: 1,
		},
		{
			name: "manager reports no monitor and the fallback has none either",
			hw: makeHardwareBattery(hardware.BatteryStatus{
				AbsentReason: hardware.AbsentReasonNoMonitor,
			}),
			battery:      &mockBatteryProvider{status: &system.BatteryStatus{ChargePercent: -1}},
			want:         &v1.BatteryStatus{ChargePercent: -1},
			monitorReads: 1,
		},
		{
			name: "monitor unreachable and fallback read fails: absent",
			hw: makeHardwareBattery(hardware.BatteryStatus{
				AbsentReason: hardware.AbsentReasonMonitorUnreachable, BusMV: 11400,
			}),
			battery:      &mockBatteryProvider{err: errors.New("i2c read failed")},
			want:         &v1.BatteryStatus{ChargePercent: -1},
			monitorReads: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestDashboardService()
			svc.Battery = tt.battery
			svc.Hardware = tt.hw

			resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
			require.NoError(t, err)
			require.NotNil(t, resp.Battery)
			assert.Equal(t, tt.want.Present, resp.Battery.Present)
			assert.InDelta(t, tt.want.VoltageVolts, resp.Battery.VoltageVolts, 0.001)
			assert.InDelta(t, tt.want.CurrentAmps, resp.Battery.CurrentAmps, 0.001)
			assert.InDelta(t, tt.want.PowerWatts, resp.Battery.PowerWatts, 0.001)
			assert.InDelta(t, tt.want.ChargePercent, resp.Battery.ChargePercent, 0.001)
			assert.Equal(t, tt.want.CellCount, resp.Battery.CellCount)
			assert.Equal(t, 1, tt.hw.getCalls(), "manager status read once")
			assert.Equal(t, tt.monitorReads, tt.battery.getCalls())
		})
	}
}

// TestDashboardService_GetDashboardStatus_RealHardwareManager feeds the
// dashboard from a real, telemetry-only hardware manager (actuation stays
// off) sampling a fake INA228.
func TestDashboardService_GetDashboardStatus_RealHardwareManager(t *testing.T) {
	mgr := hardware.New(hardware.Options{
		Log:     zerolog.Nop(),
		BoardID: board.GhostnetV1,
		Battery: &fakeMonitorReader{reading: system.MonitorReading{
			Present: true, BusMillivolts: 11800, CurrentMilliamp: 420, HasCurrent: true,
			PowerMicrowatts: 4_956_000, HasPower: true,
		}},
		SeriesCells: 3,
	})
	t.Cleanup(mgr.Close)
	mgr.Step(context.Background())
	require.True(t, mgr.Status().Battery.Present)

	monitor := &mockBatteryProvider{status: &system.BatteryStatus{Present: true, VoltageVolts: 1, ChargePercent: 1}}
	svc := newTestDashboardService()
	svc.Battery = monitor
	svc.Hardware = mgr

	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	b := resp.GetBattery()
	assert.True(t, b.GetPresent())
	assert.InDelta(t, 11.8, b.GetVoltageVolts(), 0.001)
	assert.InDelta(t, 0.42, b.GetCurrentAmps(), 0.001)
	assert.InDelta(t, 4.956, b.GetPowerWatts(), 0.001)
	assert.InDelta(t, -1, b.GetChargePercent(), 0.001)
	assert.Equal(t, uint32(3), b.GetCellCount())
	assert.Zero(t, monitor.getCalls(), "the manager replaces the monitor read")
}

func TestDashboardService_GetDashboardStatus_NetworkSummary(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	ns := resp.NetworkSummary
	require.NotNil(t, ns)
	require.Len(t, ns.Entries, 5) // WAN, LAN, HaLow, BATMAN, Tailscale

	// WAN — disconnected
	wan := ns.Entries[0]
	assert.Equal(t, "eth0", wan.InterfaceName)
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_DISCONNECTED, wan.State)
	assert.Equal(t, "Disconnected", wan.Detail)
	assert.Equal(t, uint64(1024), wan.RxBytes)
	assert.Equal(t, uint64(2048), wan.TxBytes)

	// LAN — connected with IP
	lan := ns.Entries[1]
	assert.Equal(t, "br-ahwlan", lan.InterfaceName)
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_CONNECTED, lan.State)
	assert.Equal(t, "10.41.25.72/16", lan.Detail)
	assert.Equal(t, uint64(142300000), lan.RxBytes)
	assert.Equal(t, uint64(87600000), lan.TxBytes)

	// HaLow Mesh — 3 neighbors
	mesh := ns.Entries[2]
	assert.Contains(t, mesh.DisplayName, "HaLow Mesh")
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_CONNECTED, mesh.State)
	assert.Contains(t, mesh.Detail, "3 neighbors")
	assert.Equal(t, uint64(12345678), mesh.RxBytes)
	assert.Equal(t, uint64(9876543), mesh.TxBytes)

	// BATMAN — 4 originators
	batman := ns.Entries[3]
	assert.Contains(t, batman.DisplayName, "BATMAN")
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_CONNECTED, batman.State)
	assert.Contains(t, batman.Detail, "4 originators")
	assert.Equal(t, uint64(5_000_000_000), batman.RxBytes)
	assert.Equal(t, uint64(3_000_000_000), batman.TxBytes)

	// Tailscale — not connected
	ts := ns.Entries[4]
	assert.Contains(t, ts.DisplayName, "Tailscale")
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_NOT_CONNECTED, ts.State)
	assert.Equal(t, uint64(555), ts.RxBytes)
	assert.Equal(t, uint64(666), ts.TxBytes)
}

func TestDashboardService_GetDashboardStatus_ActiveServices(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	require.Len(t, resp.ActiveServices, 6)

	// openmanetd — running
	assert.Equal(t, "openmanetd", resp.ActiveServices[0].Name)
	assert.Equal(t, v1.ServiceStatus_SERVICE_STATUS_RUNNING, resp.ActiveServices[0].Status)
	assert.Equal(t, int32(1842), resp.ActiveServices[0].Pid)

	// batadv-vis — stopped
	assert.Equal(t, "batadv-vis", resp.ActiveServices[5].Name)
	assert.Equal(t, v1.ServiceStatus_SERVICE_STATUS_STOPPED, resp.ActiveServices[5].Status)
	assert.Equal(t, int32(0), resp.ActiveServices[5].Pid)
}

func TestDashboardService_ExecuteQuickAction_Reboot(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.ExecuteQuickAction(context.Background(), &v1.ExecuteQuickActionRequest{
		Action: v1.QuickAction_QUICK_ACTION_REBOOT_DEVICE,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Contains(t, *resp.Message, "Reboot")
}

func TestDashboardService_ExecuteQuickAction_RestartNetwork(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.ExecuteQuickAction(context.Background(), &v1.ExecuteQuickActionRequest{
		Action: v1.QuickAction_QUICK_ACTION_RESTART_NETWORK,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestDashboardService_ExecuteQuickAction_RestartOpenmanetd(t *testing.T) {
	svc := newTestDashboardService()
	resp, err := svc.ExecuteQuickAction(context.Background(), &v1.ExecuteQuickActionRequest{
		Action: v1.QuickAction_QUICK_ACTION_RESTART_OPENMANETD,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestDashboardService_ExecuteQuickAction_UnknownAction(t *testing.T) {
	svc := newTestDashboardService()
	_, err := svc.ExecuteQuickAction(context.Background(), &v1.ExecuteQuickActionRequest{
		Action: v1.QuickAction_QUICK_ACTION_UNSPECIFIED,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestDashboardService_ExecuteQuickAction_Error(t *testing.T) {
	svc := newTestDashboardService()
	svc.Actions = &mockQuickActionExecutor{rebootErr: errors.New("permission denied")}
	_, err := svc.ExecuteQuickAction(context.Background(), &v1.ExecuteQuickActionRequest{
		Action: v1.QuickAction_QUICK_ACTION_REBOOT_DEVICE,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

func TestDashboardService_GetDashboardStatus_ServiceCheckError(t *testing.T) {
	svc := newTestDashboardService()
	svc.Services = &mockServiceChecker{err: errors.New("service check failed")}
	_, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

func TestDashboardService_GetDashboardStatus_NoTailscaleProvider(t *testing.T) {
	svc := newTestDashboardService()
	svc.Tailscale = nil
	// Also remove tailscale0 from interfaces so no tailscale entry shows
	svc.Interfaces = &mockInterfaceProvider{
		ifaces: []network.NetworkInterfaceInfo{
			{Name: "eth0", LinkType: network.LinkTypeEthernet, State: network.OperStateDown},
		},
	}
	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)
	// Should not contain tailscale entry
	for _, entry := range resp.NetworkSummary.Entries {
		assert.NotContains(t, entry.DisplayName, "Tailscale")
	}
}

func TestDashboardService_GetDashboardStatus_IntegrationViaConnectRPC(t *testing.T) {
	svc := newTestDashboardService()

	mux := http.NewServeMux()
	mux.Handle(dashboardconnect.NewDashboardServiceHandler(svc))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := dashboardconnect.NewDashboardServiceClient(srv.Client(), srv.URL)

	resp, err := client.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "HaLowLink2-6cff", resp.DeviceInfo.Hostname)
	assert.Len(t, resp.ActiveServices, 6)
	assert.Len(t, resp.NetworkSummary.Entries, 5)
}

func TestDashboardService_ExecuteQuickAction_IntegrationViaConnectRPC(t *testing.T) {
	svc := newTestDashboardService()

	mux := http.NewServeMux()
	mux.Handle(dashboardconnect.NewDashboardServiceHandler(svc))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := dashboardconnect.NewDashboardServiceClient(srv.Client(), srv.URL)

	resp, err := client.ExecuteQuickAction(context.Background(), &v1.ExecuteQuickActionRequest{
		Action: v1.QuickAction_QUICK_ACTION_RESTART_NETWORK,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestServiceStateToProto(t *testing.T) {
	assert.Equal(t, v1.ServiceStatus_SERVICE_STATUS_RUNNING, serviceStateToProto(system.ServiceStateRunning))
	assert.Equal(t, v1.ServiceStatus_SERVICE_STATUS_STOPPED, serviceStateToProto(system.ServiceStateStopped))
	assert.Equal(t, v1.ServiceStatus_SERVICE_STATUS_UNSPECIFIED, serviceStateToProto(system.ServiceStateUnknown))
}

func findTailscaleEntry(entries []*v1.NetworkSummaryEntry) *v1.NetworkSummaryEntry {
	for _, e := range entries {
		if e.InterfaceName == "tailscale0" {
			return e
		}
	}

	return nil
}

func TestDashboardService_BuildTailscaleEntry(t *testing.T) {
	tests := []struct {
		name         string
		tailscale    TailscaleStatusProvider
		ifaces       []network.NetworkInterfaceInfo
		wantState    v1.NetworkInterfaceState
		wantDetail   string
		wantNilEntry bool
	}{
		{
			name:      "running with interface present shows connected with IP",
			tailscale: &mockTailscaleProvider{running: true},
			ifaces: []network.NetworkInterfaceInfo{
				{Name: "tailscale0", IP: "100.64.0.5/32", State: network.OperStateUp},
			},
			wantState:  v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_CONNECTED,
			wantDetail: "100.64.0.5/32",
		},
		{
			name:      "not running with interface present shows not connected",
			tailscale: &mockTailscaleProvider{running: false},
			ifaces: []network.NetworkInterfaceInfo{
				{Name: "tailscale0", IP: "100.64.0.5/32", State: network.OperStateUp},
			},
			wantState:  v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_NOT_CONNECTED,
			wantDetail: "Not connected",
		},
		{
			name:      "running without interface present shows not connected",
			tailscale: &mockTailscaleProvider{running: true},
			ifaces: []network.NetworkInterfaceInfo{
				{Name: "eth0", State: network.OperStateUp},
			},
			wantState:  v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_NOT_CONNECTED,
			wantDetail: "Not connected",
		},
		{
			name:      "nil provider with interface present shows not connected",
			tailscale: nil,
			ifaces: []network.NetworkInterfaceInfo{
				{Name: "tailscale0", IP: "100.64.0.5/32", State: network.OperStateUp},
			},
			wantState:  v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_NOT_CONNECTED,
			wantDetail: "Not connected",
		},
		{
			name:         "nil provider without interface returns nil",
			tailscale:    nil,
			ifaces:       []network.NetworkInterfaceInfo{{Name: "eth0"}},
			wantNilEntry: true,
		},
		{
			name:      "running with interface present but empty IP",
			tailscale: &mockTailscaleProvider{running: true},
			ifaces: []network.NetworkInterfaceInfo{
				{Name: "tailscale0", IP: "", State: network.OperStateUp},
			},
			wantState:  v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_CONNECTED,
			wantDetail: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &DashboardService{
				Log:       zerolog.Nop(),
				Tailscale: tc.tailscale,
			}

			entry := svc.buildTailscaleEntry(tc.ifaces)

			if tc.wantNilEntry {
				assert.Nil(t, entry)

				return
			}

			require.NotNil(t, entry)
			assert.Equal(t, "tailscale0", entry.InterfaceName)
			assert.Equal(t, "Tailscale (tailscale0)", entry.DisplayName)
			assert.Equal(t, tc.wantState, entry.State)
			assert.Equal(t, tc.wantDetail, entry.Detail)
		})
	}
}

func TestDashboardService_GetDashboardStatus_TailscaleRunning(t *testing.T) {
	svc := newTestDashboardService()
	svc.Tailscale = &mockTailscaleProvider{running: true}
	svc.Interfaces = &mockInterfaceProvider{
		ifaces: []network.NetworkInterfaceInfo{
			{Name: "eth0", LinkType: network.LinkTypeEthernet, State: network.OperStateDown},
			{Name: "tailscale0", LinkType: network.LinkTypeUnknown, State: network.OperStateUp, IP: "100.64.0.5/32"},
		},
	}

	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	entry := findTailscaleEntry(resp.NetworkSummary.Entries)
	require.NotNil(t, entry, "tailscale entry should be present")
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_CONNECTED, entry.State)
	assert.Equal(t, "100.64.0.5/32", entry.Detail)
}

func TestDashboardService_GetDashboardStatus_TailscaleNotRunning(t *testing.T) {
	svc := newTestDashboardService()
	svc.Tailscale = &mockTailscaleProvider{running: false}
	svc.Interfaces = &mockInterfaceProvider{
		ifaces: []network.NetworkInterfaceInfo{
			{Name: "eth0", LinkType: network.LinkTypeEthernet, State: network.OperStateDown},
			{Name: "tailscale0", LinkType: network.LinkTypeUnknown, State: network.OperStateUp, IP: "100.64.0.5/32"},
		},
	}

	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)

	entry := findTailscaleEntry(resp.NetworkSummary.Entries)
	require.NotNil(t, entry, "tailscale entry should be present")
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_NOT_CONNECTED, entry.State)
	assert.Equal(t, "Not connected", entry.Detail)
}

// TestDashboardService_GetDashboardStatus_PartialDeviceInfoFailure verifies
// that an error from one of the SysInfo getters does not abort the RPC —
// the partial response still surfaces fields populated by other providers.
// Without this, a single broken sysinfo path on a target device would blank
// the entire dashboard instead of just one row.
func TestDashboardService_GetDashboardStatus_PartialDeviceInfoFailure(t *testing.T) {
	svc := newTestDashboardService()
	svc.SysInfo = &mockSysInfoProvider{err: errors.New("proc fs unreadable")}

	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err, "RPC must succeed even when SysInfo fails")
	require.NotNil(t, resp.DeviceInfo)
	require.NotNil(t, resp.SystemResources)

	// SysInfo-sourced fields are zero-valued.
	assert.Empty(t, resp.DeviceInfo.Hostname)
	assert.Empty(t, resp.DeviceInfo.Kernel)
	assert.Empty(t, resp.DeviceInfo.Architecture)

	// Fields sourced from other providers are still populated.
	assert.Equal(t, "MorseMicro HaLowLink 2", resp.DeviceInfo.Model)
	assert.Equal(t, "OpenWrt 23.05.3 / OpenMANET 1.7.0", resp.DeviceInfo.Firmware)

	// SystemResources LocalTime is always set; CPU/memory are zero.
	require.NotNil(t, resp.SystemResources.LocalTime)
	assert.InDelta(t, 0.0, float64(resp.SystemResources.CpuLoadPercent), 0.001)
	assert.Equal(t, int64(0), resp.SystemResources.MemoryTotalBytes)
}

// TestDashboardService_GetDashboardStatus_PartialNetworkFailure verifies that
// a failure in one network builder (mesh count) does not blank the rest of
// the network summary. The mesh entry should be present with a fallback
// detail; WAN/LAN/BATMAN entries should still be populated.
func TestDashboardService_GetDashboardStatus_PartialNetworkFailure(t *testing.T) {
	svc := newTestDashboardService()
	svc.Wifi = &mockWifiProvider{err: errors.New("netlink gone")}

	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err, "RPC must succeed even when mesh count fails")

	ns := resp.NetworkSummary
	require.NotNil(t, ns)
	require.GreaterOrEqual(t, len(ns.Entries), 4, "WAN, LAN, mesh (fallback), BATMAN must all appear")

	// Find the HaLow Mesh entry — it should have the fallback detail rather
	// than being omitted entirely.
	var meshEntry *v1.NetworkSummaryEntry

	for _, entry := range ns.Entries {
		if entry.InterfaceName == "phy1-ap0" {
			meshEntry = entry

			break
		}
	}

	require.NotNil(t, meshEntry, "mesh entry must remain in summary even when neighbor count fails")
	assert.Equal(t, v1.NetworkInterfaceState_NETWORK_INTERFACE_STATE_DISCONNECTED, meshEntry.State)
	assert.Equal(t, "Unknown", meshEntry.Detail)
}

// TestDashboardService_GetDashboardStatus_InterfaceListFailureSurvives verifies
// that the network summary failing entirely (interface list error) does not
// abort the dashboard — the rest of the response (DeviceInfo, SystemResources,
// ActiveServices) is still delivered.
func TestDashboardService_GetDashboardStatus_InterfaceListFailureSurvives(t *testing.T) {
	svc := newTestDashboardService()
	svc.Interfaces = &mockInterfaceProvider{err: errors.New("rtnetlink unavailable")}

	resp, err := svc.GetDashboardStatus(context.Background(), &emptypb.Empty{})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Network summary is empty (no entries) but the rest survives.
	require.NotNil(t, resp.NetworkSummary)
	assert.Empty(t, resp.NetworkSummary.Entries)
	assert.Equal(t, "HaLowLink2-6cff", resp.DeviceInfo.Hostname)
	assert.Len(t, resp.ActiveServices, 6)
}

func TestDashboardService_MonitoredServices_Default(t *testing.T) {
	svc := newTestDashboardService()
	svc.MonitoredServices = nil
	assert.Equal(t, system.DefaultMonitoredServices(), svc.monitoredServices())
}

func TestDashboardService_MonitoredServices_Custom(t *testing.T) {
	svc := newTestDashboardService()
	svc.MonitoredServices = []string{"custom-svc"}
	assert.Equal(t, []string{"custom-svc"}, svc.monitoredServices())
}
