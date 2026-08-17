package registry

import (
	"context"
	"testing"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
)

func TestOpenErrorReturnsNilSourceInterface(t *testing.T) {
	opened, err := Open(context.Background(), config.Source{
		Name: "unreachable", Type: "postgis",
		DSN: "postgres://reader:secret@127.0.0.1:1/gis", Table: "public.roads",
	})
	if err == nil {
		if opened != nil {
			opened.Close()
		}
		t.Fatal("expected unreachable PostGIS source to fail")
	}
	if opened != nil {
		t.Fatalf("failed open returned a non-nil source interface: %#v", opened)
	}
}
