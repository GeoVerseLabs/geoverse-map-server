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

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/server"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

var version = "dev" // injected via -ldflags "-X main.version=..."

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML configuration")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("geoverse", version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*configPath, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
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

	server.Version = version
	srv := server.New(cfg, reg, log)
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
