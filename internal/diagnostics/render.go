package diagnostics

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func Write(w io.Writer, report Report, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "text":
		_, err := io.WriteString(w, FormatText(report))
		return err
	default:
		return fmt.Errorf("unknown diagnostic format %q", format)
	}
}

func FormatText(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "geoverse %s: %s\n", report.Mode, report.Status)
	fmt.Fprintf(&b, "config: %s\n", report.ConfigPath)
	for _, finding := range report.Findings {
		if finding.Source == "" {
			fmt.Fprintf(&b, "[%s] %s: %s\n", finding.Severity, finding.Code, finding.Message)
		} else {
			fmt.Fprintf(&b, "[%s] %s (%s): %s\n", finding.Severity, finding.Code, finding.Source, finding.Message)
		}
	}
	for _, src := range report.Sources {
		fmt.Fprintf(&b, "source %s (%s): %s\n", src.Name, src.Type, src.Status)
		if src.Detail != "" {
			fmt.Fprintf(&b, "  detail: %s\n", src.Detail)
		}
		if src.Asset != nil {
			fmt.Fprintf(&b, "  asset: %s -> %s (%d bytes)\n", src.Asset.RequestedPath, src.Asset.ResolvedPath, src.Asset.Size)
		}
		if len(src.Capabilities) > 0 {
			fmt.Fprintf(&b, "  capabilities: %s\n", strings.Join(src.Capabilities, ", "))
		}
		if src.Tile != nil {
			fmt.Fprintf(&b, "  tile: %s z%d-%d bounds=%v\n", src.Tile.Format, src.Tile.MinZoom, src.Tile.MaxZoom, src.Tile.Bounds)
		}
		if src.Feature != nil {
			fmt.Fprintf(&b, "  features: %s bounds=%v\n", src.Feature.Title, src.Feature.Bounds)
		}
	}
	return b.String()
}
