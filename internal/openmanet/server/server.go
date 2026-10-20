package server

import (
	"context"
	"math/rand"
	"net/http"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"
	"connectrpc.com/validate"
	blosconnect "github.com/openmanet/openmanetd/internal/api/openmanet/blos/v1/blosv1connect"
	chanmigconnect "github.com/openmanet/openmanetd/internal/api/openmanet/chanmig/v1/chanmigv1connect"
	commsconnect "github.com/openmanet/openmanetd/internal/api/openmanet/comms/v1/commsv1connect"
	dashboardconnect "github.com/openmanet/openmanetd/internal/api/openmanet/dashboard/v1/dashboardv1connect"
	gnssconnect "github.com/openmanet/openmanetd/internal/api/openmanet/gnss/v1/gnssv1connect"
	hardwareconnect "github.com/openmanet/openmanetd/internal/api/openmanet/hardware/v1/hardwarev1connect"
	logsconnect "github.com/openmanet/openmanetd/internal/api/openmanet/logs/v1/logsv1connect"
	meshjoinconnect "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_join/v1/mesh_joinv1connect"
	meshtopoconnect "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_topology/v1/mesh_topologyv1connect"
	niconnect "github.com/openmanet/openmanetd/internal/api/openmanet/network_interface/v1/network_interfacev1connect"
	radioconnect "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1/radiov1connect"
	services "github.com/openmanet/openmanetd/internal/api/openmanet/service/v1/servicev1connect"
	setupconnect "github.com/openmanet/openmanetd/internal/api/openmanet/setup/v1/setupv1connect"
	supconnect "github.com/openmanet/openmanetd/internal/api/openmanet/sysupgrade/v1/sysupgradev1connect"
	wificonfigconnect "github.com/openmanet/openmanetd/internal/api/openmanet/wifi_config/v1/wifi_configv1connect"
	"github.com/openmanet/openmanetd/internal/auth"
	batmanadv "github.com/openmanet/openmanetd/internal/batman-adv"
	"github.com/openmanet/openmanetd/internal/blos"
	"github.com/openmanet/openmanetd/internal/comms"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/iwinfo"
	"github.com/openmanet/openmanetd/internal/logs"
	"github.com/openmanet/openmanetd/internal/mgmt"
	"github.com/openmanet/openmanetd/internal/network"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/sysupgrade"
	"github.com/openmanet/openmanetd/internal/util/logger"
	"github.com/rs/cors"
	"github.com/rs/zerolog"
)

type APIServer struct {
	Cfg                   *config.Config
	Log                   zerolog.Logger
	DB                    *models.Queries
	ApiServer             *http.Server
	Wifi                  mgmt.WirelessProvider
	GPS                   *gpsd.GPSService
	BLOSManager           blos.BLOSLifecycle
	CommsManager          comms.CommsLifecycle
	Mixer                 handlers.AudioMixer
	Interfaces            handlers.InterfaceProvider
	DHCP                  handlers.DHCPConfigProvider
	Leases                handlers.LeaseProvider
	Tailscale             handlers.TailscaleStatusProvider
	MeshDeltaTracker      *handlers.DeltaTracker
	MeshOrigProvider      batmanadv.OriginatorTopologyProvider
	MeshVisProvider       batmanadv.VisProvider
	MeshNeighborsProvider batmanadv.MeshNeighborsProvider
	BatctlSnapshotter     *handlers.BatctlSnapshotter
	SystemSnapshotter     *handlers.SystemSnapshotter
	Logread               logs.LogProvider
	Dmesg                 logs.LogProvider
	SessionStore          *auth.SessionStore
	Authenticator         auth.Authenticator
	Sysupgrade            *sysupgrade.Manager
	// Hardware is the V1 hardware manager; nil when hardware.enable is
	// false. Keep it an interface value that is nil, not a typed nil
	// pointer, so the handler can detect the disabled case.
	Hardware handlers.HardwareManager
	// Chanmig is the running channel-migration node; nil when chanmig is
	// disabled or failed to start. Like Hardware it must be a nil
	// interface, not a typed nil pointer.
	Chanmig handlers.ChannelMigrator
	// Setup wizard dependencies. Each is constructed in
	// internal/openmanet/openmanet.go and passed through here so the
	// server registration block can stay declarative.
	SetupUCIReader      network.ConfigReader
	SetupSnapshotter    handlers.UCISnapshotter
	SetupPasswordSetter auth.PasswordSetter
	SetupHostnameSetter handlers.HostnameSetter
	SetupReloader       system.ServiceReloader
	SetupRNG            *rand.Rand
	// RadioCapacity is the radio capacity gate NewAPIServer builds. Its
	// decision history is served by RadioService; NewBandAdmitter (the
	// pathsel capacity input) is exposed here for later wiring. Callers do
	// not set it.
	RadioCapacity *radiogate.Gate
	AuthEnabled   bool
}

func NewAPIServer(cfg APIServer) *APIServer {
	var (
		api                 = http.NewServeMux()
		validateInterceptor = validate.NewInterceptor()
	)

	// Wrap the DHCP lease provider with a short TTL cache so concurrent
	// ListActiveDHCPLeases / GetDHCPServerConfig / ListConnectedClients
	// calls share a single ubus invocation.
	leases := cfg.Leases
	if leases != nil {
		leases = handlers.NewCachedLeaseProvider(leases, handlers.DefaultDHCPLeaseCacheTTL)
	}

	// Wrap the netlink interface provider with a TTL cache so
	// ListNetworkInterfaces and Dashboard.buildNetworkSummary share one
	// LinkList+AddrList walk per interval.
	interfaces := cfg.Interfaces
	if interfaces != nil {
		interfaces = handlers.NewCachedInterfaceProvider(interfaces, handlers.DefaultInterfaceCacheTTL)
	}

	// Wrap the wireless netlink provider with a TTL cache so
	// Interfaces() + per-iface StationInfo() are fetched once per TTL
	// and shared across all handlers that read wifi state. The daemon
	// normally passes an already-cached provider (shared with the
	// instrumentation snapshotter), which is reused as is.
	wifi := handlers.EnsureCachedWireless(cfg.Wifi)

	nodeSvc := &handlers.NodeService{
		DB:  cfg.DB,
		Log: cfg.Log,
	}
	api.Handle(services.NewNodeServiceHandler(nodeSvc, connect.WithInterceptors(validateInterceptor)))

	ifaceSvc := &handlers.InterfaceService{
		Log:  cfg.Log,
		Wifi: wifi,
	}
	api.Handle(services.NewInterfaceServiceHandler(ifaceSvc, connect.WithInterceptors(validateInterceptor)))

	meshSvc := &handlers.MeshService{
		Log:  cfg.Log,
		Wifi: wifi,
	}
	if cfg.BatctlSnapshotter != nil {
		meshSvc.ParseBatHosts = cfg.BatctlSnapshotter.ParseBatHosts
		meshSvc.GetMeshNeighbors = cfg.BatctlSnapshotter.GetMeshNeighbors
	}

	api.Handle(services.NewMeshNeighborServiceHandler(meshSvc, connect.WithInterceptors(validateInterceptor)))

	statusSvc := &handlers.StatusService{
		Cfg:  cfg.Cfg,
		Log:  cfg.Log,
		Wifi: wifi,
		GPS:  cfg.GPS,
	}
	if cfg.BatctlSnapshotter != nil {
		statusSvc.GetMeshCfg = cfg.BatctlSnapshotter.GetMeshConfig
		statusSvc.GetMeshGateways = cfg.BatctlSnapshotter.GetMeshGateways
	}

	api.Handle(services.NewStatusServiceHandler(statusSvc, connect.WithInterceptors(validateInterceptor)))

	api.Handle(commsconnect.NewCommsServiceHandler(&handlers.CommsService{
		Cfg:          cfg.Cfg,
		Log:          cfg.Log,
		CommsManager: cfg.CommsManager,
		Service:      comms.Default,
		Mixer:        cfg.Mixer,
	}, connect.WithInterceptors(validateInterceptor)))

	api.Handle(blosconnect.NewBLOSServiceHandler(&handlers.BLOSService{
		Cfg:         cfg.Cfg,
		Log:         cfg.Log,
		BLOSManager: cfg.BLOSManager,
	}, connect.WithInterceptors(validateInterceptor)))

	api.Handle(niconnect.NewNetworkInterfaceServiceHandler(&handlers.NetworkInterfaceService{
		Log:        cfg.Log,
		Interfaces: interfaces,
		DHCP:       cfg.DHCP,
		Leases:     leases,
	}, connect.WithInterceptors(validateInterceptor)))

	var dashOrigProvider batmanadv.OriginatorProvider = &batmanadv.BatctlOriginatorProvider{}
	if cfg.BatctlSnapshotter != nil {
		dashOrigProvider = cfg.BatctlSnapshotter
	}

	var (
		dashSysInfo  system.SysInfoProvider = &system.LinuxSysInfo{}
		dashServices system.ServiceChecker  = &system.InitDServiceChecker{}
	)
	if cfg.SystemSnapshotter != nil {
		dashSysInfo = cfg.SystemSnapshotter
		dashServices = cfg.SystemSnapshotter
	}

	dashBoard := handlers.NewCachedBoardProvider(&handlers.DefaultBoardProvider{})

	dashBattery := system.NewHwmonBatteryProviderForBoard("")
	if b, err := dashBoard.GetBoard(); err == nil && b != nil {
		dashBattery = system.NewHwmonBatteryProviderForBoard(b.Model.ID)
	}

	api.Handle(dashboardconnect.NewDashboardServiceHandler(&handlers.DashboardService{
		Log:         cfg.Log,
		Board:       dashBoard,
		SysInfo:     dashSysInfo,
		Firmware:    handlers.NewCachedFirmwareProvider(&system.OpenWrtFirmwareProvider{}),
		Interfaces:  interfaces,
		Wifi:        &handlers.DefaultWifiStationProvider{Wifi: wifi},
		Originators: dashOrigProvider,
		Tailscale:   cfg.Tailscale,
		Services:    dashServices,
		Actions:     &system.InitDActionExecutor{},
		Battery:     system.NewCachedBatteryProvider(dashBattery, nil),
		Hardware:    cfg.Hardware,
	}, connect.WithInterceptors(validateInterceptor)))

	api.Handle(gnssconnect.NewGNSSServiceHandler(&handlers.GNSSService{
		Cfg: cfg.Cfg,
		Log: cfg.Log,
		GPS: cfg.GPS,
	}, connect.WithInterceptors(validateInterceptor)))

	// HardwareService exposes V1 battery telemetry, radio state and the
	// recovery controls. Control RPCs record the session username; with
	// auth enabled they refuse calls that carry none.
	api.Handle(hardwareconnect.NewHardwareServiceHandler(&handlers.HardwareService{
		Log:            cfg.Log.With().Str("service", "hardware").Logger(),
		Manager:        cfg.Hardware,
		RequireSession: cfg.AuthEnabled,
	}, connect.WithInterceptors(validateInterceptor)))

	// SetupService runs the first-boot wizard. The middleware exempts
	// both RPCs from auth (see auth/middleware.go isAPISkipPath); the
	// handler enforces its own enabled/complete gate as
	// defense-in-depth.
	api.Handle(setupconnect.NewSetupServiceHandler(&handlers.SetupService{
		Cfg:            cfg.Cfg,
		Log:            cfg.Log.With().Str("service", "setup").Logger(),
		UCI:            cfg.SetupUCIReader,
		Snapshotter:    cfg.SetupSnapshotter,
		PasswordSetter: cfg.SetupPasswordSetter,
		HostnameSetter: cfg.SetupHostnameSetter,
		Reloader:       cfg.SetupReloader,
		Iwinfo:         iwinfo.NewClient(),
		WirelessStatus: network.NewDefaultWirelessStatusProvider(),
		Interfaces:     interfaces,
		RNG:            cfg.SetupRNG,
	}, connect.WithInterceptors(validateInterceptor)))

	meshTopoSvc := &handlers.MeshTopologyService{
		Log:               cfg.Log,
		VisProvider:       cfg.MeshVisProvider,
		OrigProvider:      cfg.MeshOrigProvider,
		NeighborsProvider: cfg.MeshNeighborsProvider,
		DeltaTracker:      cfg.MeshDeltaTracker,
		BatInterface:      cfg.Cfg.GetAlfredBatInterface(),
		StatusSvc:         statusSvc,
		NodeSvc:           nodeSvc,
		MeshSvc:           meshSvc,
		InterfaceSvc:      ifaceSvc,
	}
	if cfg.BatctlSnapshotter != nil {
		meshTopoSvc.ParseBatHosts = cfg.BatctlSnapshotter.ParseBatHosts
		meshTopoSvc.GetMeshGateways = cfg.BatctlSnapshotter.GetMeshGateways
	}

	api.Handle(meshtopoconnect.NewMeshTopologyServiceHandler(meshTopoSvc, connect.WithInterceptors(validateInterceptor)))

	wifiSvc := &handlers.WifiConfigService{
		Log:            cfg.Log,
		IwinfoClient:   iwinfo.NewClient(),
		Wifi:           wifi,
		WirelessStatus: network.NewDefaultWirelessStatusProvider(),
		ConfigReader:   network.NewUCIWirelessConfigReader(),
		DHCPLeases:     leases,
	}
	if cfg.BatctlSnapshotter != nil {
		wifiSvc.ParseBatHosts = cfg.BatctlSnapshotter.ParseBatHosts
		wifiSvc.GetMeshNeighbors = cfg.BatctlSnapshotter.GetMeshNeighbors
	}

	// Radio capacity gate (GHO-59): radio writes from the settings page
	// and mesh join are checked against the node's PHY capacity before
	// staging. Advisory by default; see radio.capacityGate.
	radioCapacity, radioInventory := newRadioCapacityGate(cfg.Cfg, cfg.Log.With().Str("component", "radio_capacity").Logger(),
		wifiSvc.ConfigReader, wifiSvc.WirelessStatus, wifiSvc.IwinfoClient)
	wifiSvc.Capacity = radioCapacity

	api.Handle(wificonfigconnect.NewWifiConfigServiceHandler(wifiSvc, connect.WithInterceptors(validateInterceptor)))

	// RadioService (GHO-51) exposes the capacity gate's mode, PHY
	// inventory and decision history read-only. Path selection is not
	// wired to live traffic yet, so Paths stays nil and ListPathDecisions
	// returns an empty list. Session-gated like every other settings read
	// (not in isAPISkipPath).
	api.Handle(radioconnect.NewRadioServiceHandler(&handlers.RadioService{
		Log:       cfg.Log.With().Str("service", "radio").Logger(),
		Gate:      radioCapacity,
		Inventory: radioInventory,
	}, connect.WithInterceptors(validateInterceptor)))

	// ChannelMigrationService (GHO-52) reports the channel-migration
	// node's state and, on coordinators, signs new channel plans. With
	// chanmig disabled the status RPC reports enabled=false. Session-gated
	// (not in isAPISkipPath); ProposeChannelPlan also records the caller
	// and refuses calls without one when auth is enabled.
	api.Handle(chanmigconnect.NewChannelMigrationServiceHandler(&handlers.ChannelMigrationService{
		Log:            cfg.Log.With().Str("service", "chanmig").Logger(),
		Node:           cfg.Chanmig,
		RequireSession: cfg.AuthEnabled,
	}, connect.WithInterceptors(validateInterceptor)))

	// MeshJoinService shares this node's mesh credentials as a QR code
	// and joins the node from a scanned code. It reads the same UCI
	// wireless tree the wifi service does. With chanmig enabled the QR
	// also carries the plan-authority public keys and mesh ID; a join
	// installs received ones only where none exist yet. Session-gated
	// like every other settings RPC (not in isAPISkipPath).
	api.Handle(meshjoinconnect.NewMeshJoinServiceHandler(&handlers.MeshJoinService{
		Log:          cfg.Log.With().Str("service", "mesh_join").Logger(),
		ConfigReader: wifiSvc.ConfigReader,
		Radios:       wifiSvc,
		ChanmigTrust: &handlers.ConfigChanmigTrust{Cfg: cfg.Cfg},
	}, connect.WithInterceptors(validateInterceptor)))

	api.Handle(logsconnect.NewLogsServiceHandler(&handlers.LogsService{
		Log:     cfg.Log,
		Logread: cfg.Logread,
		Dmesg:   cfg.Dmesg,
	}, connect.WithInterceptors(validateInterceptor)))

	if cfg.Sysupgrade != nil {
		api.Handle(supconnect.NewSysupgradeServiceHandler(&handlers.SysupgradeService{
			Log:     cfg.Log.With().Str("service", "sysupgrade").Logger(),
			Manager: cfg.Sysupgrade,
		}, connect.WithInterceptors(validateInterceptor)))

		// Out-of-band binary upload for staged firmware images. Streams
		// the multipart body straight into the manager — does not ride
		// through ConnectRPC.
		api.Handle("/api/sysupgrade/upload", &handlers.SysupgradeUploadHandler{
			Log:     cfg.Log.With().Str("service", "sysupgrade-upload").Logger(),
			Manager: cfg.Sysupgrade,
		})
	}

	// Register auth endpoints. Login and logout are only available when
	// authentication is enabled. The check endpoint is always registered so
	// the frontend can determine whether auth is required.
	if cfg.Authenticator != nil && cfg.SessionStore != nil {
		authHandler := &auth.AuthHandler{
			Log:            cfg.Log.With().Str("service", "auth").Logger(),
			Authenticator:  cfg.Authenticator,
			Store:          cfg.SessionStore,
			PasswordSetter: &auth.ChpasswdSetter{},
		}
		api.HandleFunc("/auth/login", authHandler.HandleLogin)
		api.HandleFunc("/auth/logout", authHandler.HandleLogout)
		api.HandleFunc("/auth/check", authHandler.HandleCheck)
		api.HandleFunc("/auth/change-password", authHandler.HandleChangePassword)
	} else {
		// Auth disabled — always report authenticated so the frontend
		// skips the login gate.
		api.HandleFunc("/auth/check", auth.HandleCheckDisabled)
	}

	authMW := auth.NewAPIAuthMiddleware(cfg.SessionStore, cfg.AuthEnabled)

	p := new(http.Protocols)
	p.SetHTTP1(true)
	// Use h2c so we can serve HTTP/2 without TLS.
	p.SetUnencryptedHTTP2(true)
	server := http.Server{
		Addr:           cfg.Cfg.GetOpenMANETAPIAddress(),
		Handler:        withCORS(authMW(api)),
		Protocols:      p,
		ReadTimeout:    30 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 16, // 64 KB
		// WriteTimeout is intentionally omitted to support long-lived
		// streaming RPCs (e.g. CommsService audio streams).
		ErrorLog: logger.StandardLogger(cfg.Log),
	}

	return &APIServer{
		Log:           cfg.Log,
		DB:            cfg.DB,
		ApiServer:     &server,
		RadioCapacity: radioCapacity,
	}
}

func (s *APIServer) Stop(ctx context.Context) error {
	return s.ApiServer.Shutdown(ctx)
}

func withCORS(handler http.Handler) http.Handler {
	c := cors.New(cors.Options{
		// AllowOriginFunc reflects the request origin rather than using a wildcard
		// so that AllowCredentials: true is compatible with the CORS spec. This is
		// safe for a private-network device where all LAN clients are trusted.
		AllowOriginFunc:  func(_ string) bool { return true },
		AllowedMethods:   connectcors.AllowedMethods(),
		AllowedHeaders:   append(connectcors.AllowedHeaders(), "Access-Control-Request-Private-Network"),
		ExposedHeaders:   connectcors.ExposedHeaders(),
		AllowCredentials: true,
		// Crucial for PNA:
		OptionsPassthrough: false,
	})

	wrapped := c.Handler(handler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Handle the Vary header for caching safety
		w.Header().Add("Vary", "Access-Control-Request-Private-Network")

		// 2. If it's a PNA preflight request, allow it
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}

		// 3. Allow cross-origin embedding so the frontend (served from a
		//    different port with COEP: require-corp) can fetch from this server.
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")

		wrapped.ServeHTTP(w, r)
	})
}
