// Command geoverse runs the GeoVerse Map Server: a lightweight geospatial
// data distribution service (vector tiles, OGC API - Features, WMTS) over
// PostGIS, MBTiles, GeoJSON and GeoPackage sources.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/cache"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/diagnostics"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/server"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

var version = "dev" // injected via -ldflags "-X main.version=..."

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("geoverse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.yaml", "path to YAML configuration")
	showVersion := fs.Bool("version", false, "print version and exit")
	validate := fs.Bool("validate", false, "load config, open every source, then exit (0 = deployable)")
	doctor := fs.Bool("doctor", false, "run read-only deployment diagnostics and exit")
	inspect := fs.String("inspect", "", "inspect one configured source by name, or all")
	format := fs.String("format", "text", "diagnostic output format: text or json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	modes := 0
	for _, selected := range []bool{*showVersion, *validate, *doctor, *inspect != ""} {
		if selected {
			modes++
		}
	}
	if modes > 1 {
		fmt.Fprintln(stderr, "choose only one of -version, -validate, -doctor or -inspect")
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "invalid -format %q (want text or json)\n", *format)
		return 2
	}
	if *format != "text" && !*doctor && *inspect == "" {
		fmt.Fprintln(stderr, "-format is only valid with -doctor or -inspect")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "geoverse", version)
		return 0
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if *validate {
		if err := validateConfig(*configPath, log); err != nil {
			log.Error("config invalid", "error", err)
			return 1
		}
		return 0
	}
	if *doctor || *inspect != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var report diagnostics.Report
		if *doctor {
			report = diagnostics.Doctor(ctx, *configPath)
		} else {
			report = diagnostics.Inspect(ctx, *configPath, *inspect)
		}
		if err := diagnostics.Write(stdout, report, *format); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return report.ExitCode()
	}
	if err := run(*configPath, log); err != nil {
		log.Error("fatal", "error", err)
		return 1
	}
	return 0
}

// validateConfig parses the config and actually opens every source, then
// tears everything down without binding a port.
//
// Opening the sources is the point: a config that parses but points at a
// missing .mbtiles or an unreachable PostGIS is exactly the failure worth
// catching in CI or in a container's pre-start hook, rather than at 3am
// when the new pod fails its readiness probe.
func validateConfig(configPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	reg, err := registry.Build(ctx, cfg)
	if err != nil {
		return err
	}
	defer reg.Close()

	failed := false
	for name, perr := range reg.Ping(ctx) {
		if perr != nil {
			log.Error("source unreachable", "name", name, "error", perr)
			failed = true
			continue
		}
		log.Info("source ok", "name", name)
	}
	if failed {
		return errors.New("one or more sources are unreachable")
	}
	log.Info("config valid", "path", configPath, "sources", len(reg.Names()))
	return nil
}

func run(configPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	buildCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	reg, err := registry.Build(buildCtx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer reg.Close()
	for _, name := range reg.Names() {
		log.Info("source ready", "name", name)
	}

	store, err := cache.NewTiered(cfg.Cache)
	if err != nil {
		return fmt.Errorf("init cache: %w", err)
	}
	defer store.Close()
	if cfg.Cache.Disk.Enabled {
		log.Info("disk cache enabled", "dir", cfg.Cache.Disk.Dir, "ttl", cfg.Cache.Disk.TTL.String())
	}
	if cfg.Auth.Enabled {
		log.Info("api key auth enabled", "keys", len(cfg.Auth.APIKeys))
	}
	if cfg.MCP.Enabled {
		log.Info("mcp endpoint enabled", "path", cfg.MCP.Path)
	}

	server.Version = version
	srv := server.NewManaged(cfg, configPath, reg, store, log)
	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           http.TimeoutHandler(srv.Handler(), cfg.Server.Timeout, "request timed out"),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "sources", len(reg.Names()))
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
}
