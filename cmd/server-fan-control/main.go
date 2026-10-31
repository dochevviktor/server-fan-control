package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/dochevviktor/server-fan-control/internal/config"
	"github.com/dochevviktor/server-fan-control/internal/control"
	"github.com/dochevviktor/server-fan-control/internal/gpu"
	"github.com/dochevviktor/server-fan-control/internal/httpapi"
	"github.com/dochevviktor/server-fan-control/internal/ipmi"
	webassets "github.com/dochevviktor/server-fan-control/internal/web"
)

func main() {
	log.SetOutput(os.Stderr)
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr, nvml.New()); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Print(err)
		os.Exit(1)
	}
}
func runCLI(args []string, stdout, stderr io.Writer, library diagnosticNVML) (runError error) {
	f := flag.NewFlagSet("server-fan-control", flag.ContinueOnError)
	f.SetOutput(stderr)
	configPath := f.String("config", "data/config.json", "persistent configuration path (legacy JSON migrates to a draft)")
	diagnose := f.Bool("diagnose", false, "read GPU identity and temperature once without changing fan settings")
	diagnoseIPMI := f.Bool("diagnose-ipmi", false, "read local BMC identity, fan RPM and supported profile bytes without fan writes")
	enableIPMI := f.Bool("ipmi", false, "enable optional local IPMI monitoring and explicitly verified fan control")
	listen := f.String("listen", "127.0.0.1:8080", "HTTP listen address")
	tokenFile := f.String("admin-token-file", "", "mounted admin secret file (at least 32 bytes)")
	mock := f.Bool("mock", false, "simulate GPU and enabled IPMI hardware; never accesses hardware")
	noAuth := f.Bool("dev-no-auth", false, "disable login only for mock mode bound to a loopback IP")
	publicOrigin := f.String("public-origin", "", "browser origin, e.g. https://fans.example.net, when using a reverse proxy")
	secureCookies := f.Bool("secure-cookies", false, "require HTTPS for session cookies")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", f.Args())
	}
	if *diagnoseIPMI {
		if *diagnose || *mock {
			return errors.New("--diagnose-ipmi cannot be combined with --diagnose or --mock")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		reading, err := ipmi.NewLocal().Read(ctx)
		encodeErr := json.NewEncoder(stdout).Encode(reading)
		return errors.Join(err, encodeErr)
	}
	if *diagnose {
		if *mock {
			return errors.New("--diagnose uses real NVML; use --mock with the HTTP service instead")
		}
		return runDiagnostic(library, stdout)
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if *noAuth && (!*mock || ip == nil || !ip.IsLoopback()) {
		return errors.New("--dev-no-auth requires --mock and a loopback IP listen address")
	}
	var adapter gpu.Adapter = gpu.NewNVML()
	if *mock {
		adapter = gpu.NewMock()
	}
	var bmc ipmi.Adapter = ipmi.NewLocal()
	ipmiName := "ipmi.json"
	if *mock {
		bmc = ipmi.NewMock()
		ipmiName = "ipmi.mock.json"
	}
	ipmiPath := filepath.Join(filepath.Dir(*configPath), ipmiName)
	workerStarted := false
	defer func() {
		if runError == nil || workerStarted || errors.Is(runError, config.ErrLocked) {
			return
		}
		journal := "ownership.json"
		if *mock {
			journal = "ownership.mock.json"
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(*configPath), journal)); !errors.Is(err, os.ErrNotExist) {
			runError = errors.Join(runError, control.RestorePending(adapter, *configPath))
		}
		runError = errors.Join(runError, ipmi.RestorePending(bmc, ipmiPath))
	}()
	token := ""
	if !*noAuth {
		if *tokenFile == "" {
			return errors.New("--admin-token-file is required (local simulation can use --mock --dev-no-auth)")
		}
		data, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fmt.Errorf("read admin token: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if len(token) < 32 || len(token) > 4096 {
			return errors.New("admin token must contain 32..4096 bytes")
		}
	}
	store, err := config.Open(*configPath)
	if err != nil {
		return fmt.Errorf("open configuration %q: %w", *configPath, err)
	}
	releaseStore := true
	defer func() {
		if releaseStore {
			_ = store.Close()
		}
	}()
	service, err := control.New(adapter, store)
	if err != nil {
		return err
	}
	var systemFans *ipmi.Service
	if *enableIPMI {
		systemFans, err = ipmi.NewService(bmc, ipmiPath)
		if err != nil {
			return fmt.Errorf("open IPMI settings: %w", err)
		}
	} else {
		// Removing the opt-in flag must still release previously owned fans.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = ipmi.RestorePendingLocked(recoveryCtx, bmc, ipmiPath)
		cancel()
		if err != nil {
			return fmt.Errorf("restore previous IPMI ownership: %w", err)
		}
	}
	assets, err := webassets.Assets()
	if err != nil {
		return err
	}
	options := httpapi.Options{AdminToken: token, DevNoAuth: *noAuth, SecureCookies: *secureCookies, AllowedOrigin: *publicOrigin, Assets: assets}
	if systemFans != nil {
		options.IPMI = systemFans
	}
	handler, err := httpapi.New(service, options)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerStarted = true
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.Run(ctx) }()
	var ipmiDone chan error
	if systemFans != nil {
		ipmiDone = make(chan error, 1)
		go func() { ipmiDone <- systemFans.Run(ctx) }()
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Serve(listener) }()
	log.Printf("listening on %s (mock=%t)", listener.Addr(), *mock)
	var runErr error
	workerFinished := false
	ipmiFinished := systemFans == nil
	select {
	case <-ctx.Done():
	case runErr = <-workerDone:
		workerFinished = true
	case runErr = <-ipmiDone:
		ipmiFinished = true
	case runErr = <-httpDone:
		if errors.Is(runErr, http.ErrServerClosed) {
			runErr = nil
		}
	}
	cancel()
	// Bound process shutdown while making every restoration attempt before NVML
	// close. A native NVML call can hang; Go cancellation cannot interrupt it.
	deadline, done := context.WithTimeout(context.Background(), 20*time.Second)
	defer done()
	shutdownErr := server.Shutdown(deadline)
	if !workerFinished {
		select {
		case err := <-workerDone:
			runErr = errors.Join(runErr, err)
		case <-deadline.Done():
			// Retain ownership of the data directory until process exit while an
			// uninterruptible native worker might still be executing.
			releaseStore = false
			runErr = errors.Join(runErr, errors.New("controller shutdown timed out; fan restoration is unconfirmed; ownership journal retained"))
		}
	}
	if !ipmiFinished {
		select {
		case err := <-ipmiDone:
			runErr = errors.Join(runErr, err)
		case <-deadline.Done():
			releaseStore = false
			runErr = errors.Join(runErr, errors.New("IPMI shutdown timed out; automatic restoration is unconfirmed; ownership journal retained"))
		}
	}
	return errors.Join(runErr, shutdownErr)
}
