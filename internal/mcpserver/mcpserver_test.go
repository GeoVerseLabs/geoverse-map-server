package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

const testGeoJSON = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "id": 1, "properties": {"name": "beijing"},
     "geometry": {"type": "Point", "coordinates": [116.39, 39.91]}},
    {"type": "Feature", "id": 2, "properties": {"name": "shanghai"},
     "geometry": {"type": "Point", "coordinates": [121.47, 31.23]}}
  ]
}`

func testHandler(t *testing.T) *Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cities.geojson")
	if err := os.WriteFile(path, []byte(testGeoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Sources = []config.Source{{Name: "cities", Type: "geojson", Path: path}}
	reg, err := registry.Build(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	return New(reg, "test", func(*http.Request) string { return "http://example.test" })
}

// rpc posts one JSON-RPC request and decodes the response envelope.
func rpc(t *testing.T, h *Handler, body string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return out
}

// callTool invokes tools/call and returns the parsed structuredContent.
func callTool(t *testing.T, h *Handler, name, args string) (map[string]interface{}, bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	resp := rpc(t, h, body)
	if resp["error"] != nil {
		t.Fatalf("rpc error: %v", resp["error"])
	}
	result := resp["result"].(map[string]interface{})
	isErr, _ := result["isError"].(bool)
	if isErr {
		text := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
		var payload map[string]interface{}
		_ = json.Unmarshal([]byte(text), &payload)
		return payload, true
	}
	sc, _ := result["structuredContent"].(map[string]interface{})
	return sc, false
}

func TestInitialize(t *testing.T) {
	h := testHandler(t)
	resp := rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	result := resp["result"].(map[string]interface{})
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	if result["serverInfo"].(map[string]interface{})["name"] != "geoverse-map-server" {
		t.Errorf("serverInfo = %v", result["serverInfo"])
	}
}

func TestNotificationAccepted(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("notification: status = %d, want 202", rec.Code)
	}
}

func TestGetRejected(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest("GET", "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rec.Code)
	}
}

func TestToolsList(t *testing.T) {
	h := testHandler(t)
	resp := rpc(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := resp["result"].(map[string]interface{})["tools"].([]interface{})
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]interface{})["name"].(string)] = true
	}
	for _, want := range []string{"list_layers", "describe_layer", "query_features", "get_feature", "server_status"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

func TestListLayersTool(t *testing.T) {
	h := testHandler(t)
	out, isErr := callTool(t, h, "list_layers", `{}`)
	if isErr {
		t.Fatalf("unexpected error: %v", out)
	}
	layers := out["layers"].([]interface{})
	if len(layers) != 1 {
		t.Fatalf("layers = %d", len(layers))
	}
	l := layers[0].(map[string]interface{})
	if l["name"] != "cities" || !strings.Contains(l["tiles_url"].(string), "http://example.test/tiles/cities/") {
		t.Errorf("layer entry: %v", l)
	}
}

func TestQueryFeaturesTool(t *testing.T) {
	h := testHandler(t)
	out, isErr := callTool(t, h, "query_features", `{"collection":"cities","bbox":[115,39,118,41]}`)
	if isErr {
		t.Fatalf("unexpected error: %v", out)
	}
	if out["numberMatched"].(float64) != 1 {
		t.Errorf("numberMatched = %v", out["numberMatched"])
	}

	// Tool-level errors surface as isError, not RPC errors.
	out, isErr = callTool(t, h, "query_features", `{"collection":"nope"}`)
	if !isErr {
		t.Fatalf("expected tool error, got %v", out)
	}
	if !strings.Contains(out["error"].(string), "unknown collection") {
		t.Errorf("error message: %v", out["error"])
	}
}

func TestGetFeatureTool(t *testing.T) {
	h := testHandler(t)
	out, isErr := callTool(t, h, "get_feature", `{"collection":"cities","id":"1"}`)
	if isErr {
		t.Fatalf("unexpected error: %v", out)
	}
	if out["type"] != "Feature" {
		t.Errorf("feature: %v", out)
	}
	if _, isErr := callTool(t, h, "get_feature", `{"collection":"cities","id":"999"}`); !isErr {
		t.Error("expected error for missing feature")
	}
}

func TestServerStatusTool(t *testing.T) {
	h := testHandler(t)
	out, isErr := callTool(t, h, "server_status", `{}`)
	if isErr || out["status"] != "ok" {
		t.Errorf("status: %v (err=%v)", out, isErr)
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	h := testHandler(t)
	resp := rpc(t, h, `{"jsonrpc":"2.0","id":9,"method":"resources/list"}`)
	if resp["error"] == nil {
		t.Error("expected method-not-found error")
	}
	resp = rpc(t, h, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"bogus","arguments":{}}}`)
	if resp["error"] == nil {
		t.Error("expected invalid-params error for unknown tool")
	}
}
