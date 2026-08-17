package registry

import (
	"context"
	"strings"
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

// TestDataSourcePolicyRejectsBeforeConnecting uses an unreachable DSN (port
// 1) on purpose: schema/table are pure string derivations, so a
// disallowed table must be rejected by the allowlist check before the
// constructor ever tries to open a connection. If the allowlist check ran
// after connecting, this test would fail on a connection-refused error
// instead of the allowlist error it asserts on.
func TestDataSourcePolicyRejectsBeforeConnecting(t *testing.T) {
	policy := config.DataSourcePolicy{AllowedSchemas: []string{"geo"}}

	_, err := OpenWithPolicies(context.Background(), config.Source{
		Name: "denied", Type: "postgis",
		DSN: "postgres://reader:secret@127.0.0.1:1/gis", Table: "secrets.roads",
	}, config.Assets{}, policy)
	if err == nil || !containsAllowlistError(err) {
		t.Fatalf("expected an allowlist rejection, got %v", err)
	}

	_, err = OpenWithPolicies(context.Background(), config.Source{
		Name: "denied", Type: "mysql",
		DSN: "mysql://reader:secret@127.0.0.1:1/gis", Table: "secrets.roads",
	}, config.Assets{}, policy)
	if err == nil || !containsAllowlistError(err) {
		t.Fatalf("expected an allowlist rejection, got %v", err)
	}
}

func containsAllowlistError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "allowed_schemas")
}
