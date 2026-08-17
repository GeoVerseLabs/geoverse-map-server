// Package diagnostics provides read-only doctor and source-inspection reports
// shared by the CLI and future management surfaces.
package diagnostics

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

const SchemaVersion = 1

type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Source   string   `json:"source,omitempty"`
}

type AssetReport struct {
	RequestedPath string `json:"requestedPath"`
	ResolvedPath  string `json:"resolvedPath"`
	Size          int64  `json:"size"`
}

type TileReport struct {
	Format       source.TileFormat    `json:"format"`
	MinZoom      int                  `json:"minZoom"`
	MaxZoom      int                  `json:"maxZoom"`
	Bounds       [4]float64           `json:"bounds"`
	VectorLayers []source.VectorLayer `json:"vectorLayers,omitempty"`
}

type FeatureReport struct {
	Title  string     `json:"title,omitempty"`
	Bounds [4]float64 `json:"bounds"`
}

type SourceReport struct {
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Status       string         `json:"status"`
	Detail       string         `json:"detail,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Asset        *AssetReport   `json:"asset,omitempty"`
	Tile         *TileReport    `json:"tile,omitempty"`
	Feature      *FeatureReport `json:"feature,omitempty"`
}

type Report struct {
	SchemaVersion int            `json:"schemaVersion"`
	Mode          string         `json:"mode"`
	Status        string         `json:"status"`
	ConfigPath    string         `json:"configPath"`
	Findings      []Finding      `json:"findings"`
	Sources       []SourceReport `json:"sources"`
}

func (r Report) ExitCode() int {
	for _, finding := range r.Findings {
		if finding.Severity == SeverityError {
			return 1
		}
	}
	for _, src := range r.Sources {
		if src.Status == "error" {
			return 1
		}
	}
	return 0
}

func Doctor(ctx context.Context, configPath string) Report {
	report, cfg := loadReport(configPath, "doctor")
	if cfg == nil {
		return report
	}
	if !cfg.Assets.EnforceRoot {
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityWarning,
			Code:     "assets.root_not_enforced",
			Message:  "file-backed sources run in compatibility mode; configure assets.root and assets.enforce_root",
		})
	}
	if cfg.Assets.MaxFileSizeMB == 0 {
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityWarning,
			Code:     "assets.file_size_unlimited",
			Message:  "assets.max_file_size_mb is unlimited",
		})
	}
	if cfg.Assets.MaxMemoryFileSizeMB == 0 {
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityWarning,
			Code:     "assets.memory_file_size_unlimited",
			Message:  "assets.max_memory_file_size_mb is unlimited",
		})
	}
	if !cfg.DataSources.Configured() && hasDatabaseSource(cfg.Sources) {
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityWarning,
			Code:     "data_sources.allowlist_not_configured",
			Message:  "postgis/mysql sources run without a schema/table allowlist; configure data_sources.allowed_schemas or allowed_tables",
		})
	}
	for _, sc := range cfg.Sources {
		src, findings := inspectSource(ctx, cfg, sc)
		report.Sources = append(report.Sources, src)
		report.Findings = append(report.Findings, findings...)
	}
	report.Status = reportStatus(report)
	return report
}

func Inspect(ctx context.Context, configPath, selector string) Report {
	report, cfg := loadReport(configPath, "inspect")
	if cfg == nil {
		return report
	}
	selector = strings.TrimSpace(selector)
	for _, sc := range cfg.Sources {
		if selector == "all" || sc.Name == selector {
			src, findings := inspectSource(ctx, cfg, sc)
			report.Sources = append(report.Sources, src)
			report.Findings = append(report.Findings, findings...)
		}
	}
	if len(report.Sources) == 0 {
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityError,
			Code:     "source.not_found",
			Message:  fmt.Sprintf("source %q is not configured", selector),
		})
	}
	report.Status = reportStatus(report)
	return report
}

func loadReport(configPath, mode string) (Report, *config.Config) {
	report := Report{
		SchemaVersion: SchemaVersion,
		Mode:          mode,
		Status:        "ok",
		ConfigPath:    configPath,
		Findings:      []Finding{},
		Sources:       []SourceReport{},
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		report.Status = "error"
		report.Findings = append(report.Findings, Finding{
			Severity: SeverityError,
			Code:     "config.load_failed",
			Message:  err.Error(),
		})
		return report, nil
	}
	return report, cfg
}

func inspectSource(ctx context.Context, cfg *config.Config, sc config.Source) (SourceReport, []Finding) {
	report := SourceReport{Name: sc.Name, Type: sc.Type, Status: "ok"}
	if fileBacked(sc.Type) {
		asset, err := cfg.Assets.InspectAsset(sc.Path, memoryBacked(sc.Type))
		if err != nil {
			report.Status = "error"
			report.Detail = sanitizeDetail(err.Error(), sc.DSN)
			return report, nil
		}
		report.Asset = &AssetReport{
			RequestedPath: asset.RequestedPath,
			ResolvedPath:  asset.ResolvedPath,
			Size:          asset.Size,
		}
	}
	openCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	opened, err := registry.OpenWithPolicies(openCtx, sc, cfg.Assets, cfg.DataSources)
	if err == nil {
		err = opened.Ping(openCtx)
	}
	if err != nil {
		if opened != nil {
			_ = opened.Close()
		}
		report.Status = "error"
		report.Detail = sanitizeDetail(err.Error(), sc.DSN)
		return report, nil
	}
	defer opened.Close()
	if tile, ok := opened.(source.TileSource); ok {
		info := tile.TileInfo()
		report.Capabilities = append(report.Capabilities, "tiles")
		report.Tile = &TileReport{
			Format: info.Format, MinZoom: info.MinZoom, MaxZoom: info.MaxZoom,
			Bounds: info.Bounds, VectorLayers: info.VectorLayers,
		}
	}
	if features, ok := opened.(source.FeatureSource); ok {
		info := features.CollectionInfo()
		report.Capabilities = append(report.Capabilities, "features")
		report.Feature = &FeatureReport{Title: info.Title, Bounds: info.Bounds}
	}
	if _, ok := opened.(source.ArchiveSource); ok {
		report.Capabilities = append(report.Capabilities, "archive")
	}
	sort.Strings(report.Capabilities)

	var findings []Finding
	if cfg.DataSources.RequireReadOnlyRole {
		if probe, ok := opened.(source.PrivilegeProbe); ok {
			excess, err := probe.ExcessPrivileges(openCtx)
			switch {
			case err != nil:
				// Not a warning: many managed databases restrict privilege
				// catalog access even for legitimate limited accounts, so a
				// failed probe is not evidence of an over-privileged one.
				report.Detail = "privilege probe did not run: " + sanitizeDetail(err.Error(), sc.DSN)
			case len(excess) > 0:
				findings = append(findings, Finding{
					Severity: SeverityWarning,
					Code:     "data_sources.excess_privileges",
					Source:   sc.Name,
					Message: fmt.Sprintf("account for %q holds privileges beyond SELECT: %s",
						sc.Name, strings.Join(excess, ", ")),
				})
			}
		}
	}
	return report, findings
}

func hasDatabaseSource(sources []config.Source) bool {
	for _, sc := range sources {
		if sc.Type == "postgis" || sc.Type == "mysql" {
			return true
		}
	}
	return false
}

func reportStatus(report Report) string {
	if report.ExitCode() != 0 {
		return "error"
	}
	if len(report.Findings) > 0 {
		return "warning"
	}
	return "ok"
}

func fileBacked(kind string) bool {
	switch kind {
	case "geojson", "mbtiles", "pmtiles", "geopackage":
		return true
	default:
		return false
	}
}

func memoryBacked(kind string) bool {
	return kind == "geojson" || kind == "geopackage"
}

func sanitizeDetail(detail, dsn string) string {
	if dsn == "" {
		return detail
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return strings.ReplaceAll(detail, dsn, "[redacted-dsn]")
	}
	redacted := *u
	if u.User != nil {
		username := u.User.Username()
		if password, ok := u.User.Password(); ok {
			detail = strings.ReplaceAll(detail, password, "••••••")
			redacted.User = url.UserPassword(username, "••••••")
		}
	}
	return strings.ReplaceAll(detail, dsn, redacted.String())
}
