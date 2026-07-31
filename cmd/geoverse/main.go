// Command geoverse runs the GeoVerse Map Server: a lightweight geospatial
// data distribution service (vector tiles, OGC API - Features, WMTS) over
// PostGIS, MBTiles, GeoJSON and GeoPackage sources.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/cache"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/server"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

var version = "dev" // injected via -ldflags "-X main.version=..."

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML configuration")
	showVersion := flag.Bool("version", false, "print version and exit")
	validate := flag.Bool("validate", false, "load config, open every source, then exit (0 = deployable)")
	flag.Parse()

	if *showVersion {
		fmt.Println("geoverse", version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *validate {
		if err := validateConfig(*configPath, log); err != nil {
			log.Error("config invalid", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*configPath, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
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
