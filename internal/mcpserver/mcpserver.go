// Package mcpserver exposes the map server's capabilities over the Model
// Context Protocol (Streamable HTTP transport, stateless mode), so LLM
// agents and other MCP clients can discover layers and query features.
//
// The implementation is a minimal, dependency-free JSON-RPC 2.0 handler
// covering initialize / ping / tools/list / tools/call, which is the
// subset needed for tool calling per the 2025-06-18 revision of the spec.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source/registry"
)

const (
	protocolVersion = "2025-06-18"
	maxBodyBytes    = 1 << 20 // 1 MiB
)

// Handler serves MCP requests for one registry.
type Handler struct {
	reg     *registry.Registry
	version string
	// baseURL derives the externally visible URL prefix from a request,
	// used to embed tile/feature URLs in tool results.
	baseURL func(*http.Request) string
}

// New creates an MCP handler.
func New(reg *registry.Registry, version string, baseURL func(*http.Request) string) *Handler {
	return &Handler{reg: reg, version: version, baseURL: baseURL}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ServeHTTP implements the Streamable HTTP transport in stateless mode:
// every call is a POST carrying one JSON-RPC message, answered with a
// single application/json response. GET (SSE streaming) is not offered.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "MCP endpoint accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	// Notifications (no id) are acknowledged without a body.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	result, rpcErr := h.dispatch(r, &req)
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(resp)
}

func (h *Handler) dispatch(r *http.Request, req *rpcRequest) (interface{}, *rpcError) {
	switch req.Method {
	case "initialize":
		return h.initialize(req.Params), nil
	case "ping":
		return map[string]interface{}{}, nil
	case "tools/list":
		return map[string]interface{}{"tools": toolDefs()}, nil
	case "tools/call":
		return h.callTool(r, req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: fmt.Sprintf("method %q not found", req.Method)}
	}
}

func (h *Handler) initialize(params json.RawMessage) interface{} {
	// Echo the client's protocol version when it is one we can speak;
	// otherwise answer with the latest version we support.
	version := protocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil {
		switch p.ProtocolVersion {
		case "2024-11-05", "2025-03-26", "2025-06-18":
			version = p.ProtocolVersion
		}
	}
	return map[string]interface{}{
		"protocolVersion": version,
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"serverInfo": map[string]interface{}{
			"name":    "geoverse-map-server",
			"version": h.version,
		},
		"instructions": "GeoVerse Map Server: list geospatial layers, inspect their metadata and query features (GeoJSON). Tile URLs returned by the tools can be fetched over plain HTTP.",
	}
}

// schema helpers keep the tool definitions readable.
type prop map[string]interface{}

func objSchema(required []string, props map[string]prop) map[string]interface{} {
	s := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func toolDefs() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "list_layers",
			"description": "List every layer served by this instance with its type, formats, bounds, zoom range and access URLs (tiles, TileJSON, OGC API Features items).",
			"inputSchema": objSchema(nil, map[string]prop{}),
		},
		{
			"name":        "describe_layer",
			"description": "Describe one tile layer: format, zoom range, bounds, center and vector layer fields (TileJSON-style metadata).",
			"inputSchema": objSchema([]string{"layer"}, map[string]prop{
				"layer": {"type": "string", "description": "Layer name as returned by list_layers"},
			}),
		},
		{
			"name":        "query_features",
			"description": "Query features from a collection as GeoJSON. Supports an optional WGS84 bounding box filter and offset/limit paging.",
			"inputSchema": objSchema([]string{"collection"}, map[string]prop{
				"collection": {"type": "string", "description": "Collection name as returned by list_layers"},
				"bbox": {"type": "array", "items": map[string]interface{}{"type": "number"},
					"minItems": 4, "maxItems": 4,
					"description": "Bounding box [west, south, east, north] in WGS84 degrees"},
				"limit":  {"type": "integer", "description": "Maximum features to return (default 10, max 1000)"},
				"offset": {"type": "integer", "description": "Number of features to skip"},
			}),
		},
		{
			"name":        "get_feature",
			"description": "Fetch a single feature from a collection by its id, as GeoJSON.",
			"inputSchema": objSchema([]string{"collection", "id"}, map[string]prop{
				"collection": {"type": "string", "description": "Collection name"},
				"id":         {"type": "string", "description": "Feature id"},
			}),
		},
		{
			"name":        "server_status",
			"description": "Report the health of the server and each configured data source.",
			"inputSchema": objSchema(nil, map[string]prop{}),
		},
	}
}

func (h *Handler) callTool(r *http.Request, params json.RawMessage) (interface{}, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "tools/call requires params.name"}
	}
	var (
		out interface{}
		err error
	)
	ctx := r.Context()
	switch p.Name {
	case "list_layers":
		out = h.listLayers(r)
	case "describe_layer":
		out, err = h.describeLayer(p.Arguments)
	case "query_features":
		out, err = h.queryFeatures(ctx, p.Arguments)
	case "get_feature":
		out, err = h.getFeature(ctx, p.Arguments)
	case "server_status":
		out = h.serverStatus(ctx)
	default:
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", p.Name)}
	}
	if err != nil {
		return toolResult(map[string]interface{}{"error": err.Error()}, true), nil
	}
	return toolResult(out, false), nil
}

// toolResult wraps a value as an MCP tool result: JSON text content plus
// the same value as structuredContent for clients that understand it.
func toolResult(v interface{}, isError bool) map[string]interface{} {
	text, _ := json.MarshalIndent(v, "", "  ")
	res := map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": string(text)}},
		"isError": isError,
	}
	if !isError {
		res["structuredContent"] = v
	}
	return res
}

func (h *Handler) listLayers(r *http.Request) interface{} {
	base := h.baseURL(r)
	var layers []map[string]interface{}
	for _, name := range h.reg.Names() {
		e := map[string]interface{}{"name": name}
		if ts, ok := h.reg.TileSource(name); ok {
			info := ts.TileInfo()
			e["title"] = info.Title
			e["tile_format"] = string(info.Format)
			e["bounds_wgs84"] = info.Bounds
			e["min_zoom"] = info.MinZoom
			e["max_zoom"] = info.MaxZoom
			e["tiles_url"] = fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}.%s", base, name, info.Format)
			e["tilejson_url"] = fmt.Sprintf("%s/tiles/%s.json", base, name)
		}
		if _, ok := h.reg.FeatureSource(name); ok {
			e["items_url"] = fmt.Sprintf("%s/collections/%s/items", base, name)
			e["queryable"] = true
		}
		layers = append(layers, e)
	}
	return map[string]interface{}{"layers": layers}
}

func (h *Handler) describeLayer(args json.RawMessage) (interface{}, error) {
	var p struct {
		Layer string `json:"layer"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.Layer == "" {
		return nil, fmt.Errorf("argument 'layer' is required")
	}
	ts, ok := h.reg.TileSource(p.Layer)
	if !ok {
		return nil, fmt.Errorf("unknown tile layer %q", p.Layer)
	}
	info := ts.TileInfo()
	return map[string]interface{}{
		"name":          info.Name,
		"title":         info.Title,
		"description":   info.Description,
		"format":        string(info.Format),
		"min_zoom":      info.MinZoom,
		"max_zoom":      info.MaxZoom,
		"bounds_wgs84":  info.Bounds,
		"center":        info.Center,
		"vector_layers": info.VectorLayers,
	}, nil
}

func (h *Handler) queryFeatures(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var p struct {
		Collection string     `json:"collection"`
		BBox       *[]float64 `json:"bbox"`
		Limit      int        `json:"limit"`
		Offset     int        `json:"offset"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.Collection == "" {
		return nil, fmt.Errorf("argument 'collection' is required")
	}
	fs, ok := h.reg.FeatureSource(p.Collection)
	if !ok {
		return nil, fmt.Errorf("unknown collection %q", p.Collection)
	}
	q := source.FeatureQuery{Limit: 10, Offset: p.Offset}
	if p.Limit > 0 {
		q.Limit = min(p.Limit, 1000)
	}
	if p.BBox != nil {
		if len(*p.BBox) != 4 {
			return nil, fmt.Errorf("bbox must contain exactly 4 numbers [west, south, east, north]")
		}
		b := [4]float64{(*p.BBox)[0], (*p.BBox)[1], (*p.BBox)[2], (*p.BBox)[3]}
		if b[0] > b[2] || b[1] > b[3] {
			return nil, fmt.Errorf("bbox min must not exceed max")
		}
		q.BBox = &b
	}
	res, err := fs.Features(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	return map[string]interface{}{
		"type":           "FeatureCollection",
		"features":       res.Features,
		"numberMatched":  res.NumberMatched,
		"numberReturned": len(res.Features),
	}, nil
}

func (h *Handler) getFeature(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var p struct {
		Collection string `json:"collection"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.Collection == "" || p.ID == "" {
		return nil, fmt.Errorf("arguments 'collection' and 'id' are required")
	}
	fs, ok := h.reg.FeatureSource(p.Collection)
	if !ok {
		return nil, fmt.Errorf("unknown collection %q", p.Collection)
	}
	f, err := fs.Feature(ctx, p.ID)
	if errors.Is(err, source.ErrFeatureNotFound) {
		return nil, fmt.Errorf("feature %q not found in %q", p.ID, p.Collection)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup failed: %w", err)
	}
	return f, nil
}

func (h *Handler) serverStatus(ctx context.Context) interface{} {
	statuses := map[string]string{}
	healthy := true
	for name, err := range h.reg.Ping(ctx) {
		if err != nil {
			statuses[name] = err.Error()
			healthy = false
		} else {
			statuses[name] = "ok"
		}
	}
	overall := "ok"
	if !healthy {
		overall = "degraded"
	}
	return map[string]interface{}{"status": overall, "sources": statuses, "version": h.version}
}
