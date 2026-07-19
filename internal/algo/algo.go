// Package algo defines the pluggable algorithm framework: algorithms are
// named capabilities that run over the server's registered data sources
// (feature collections and routable networks). New algorithms implement
// the Algorithm interface and register themselves; the HTTP layer and the
// MCP endpoint expose every registered algorithm automatically.
package algo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/algo/network"
	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

// Env gives algorithms access to the server's data.
type Env struct {
	// Features resolves a feature source (collection) by name.
	Features func(name string) (source.FeatureSource, bool)
	// Networks resolves and caches routable graphs built from sources.
	Networks *network.Manager
}

// Descriptor documents an algorithm: name, purpose and a JSON Schema for
// its parameters. The same document is served by GET /algorithms and used
// to derive MCP tool definitions.
type Descriptor struct {
	Name        string                 `json:"name"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// Algorithm is one pluggable capability.
type Algorithm interface {
	Describe() Descriptor
	// Run executes the algorithm with JSON-encoded parameters and
	// returns a JSON-serializable result (typically GeoJSON).
	Run(ctx context.Context, env *Env, params json.RawMessage) (interface{}, error)
}

// UserError marks failures caused by the request (bad params, unknown
// layer) rather than the server; HTTP maps it to 400 instead of 500.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Errorf builds a UserError.
func Errorf(format string, args ...interface{}) error {
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

// Registry holds algorithms in registration order.
type Registry struct {
	algos map[string]Algorithm
	order []string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{algos: map[string]Algorithm{}}
}

// Register adds an algorithm; duplicate names panic (programmer error).
func (r *Registry) Register(a Algorithm) {
	name := a.Describe().Name
	if _, dup := r.algos[name]; dup {
		panic(fmt.Sprintf("algo: duplicate algorithm %q", name))
	}
	r.algos[name] = a
	r.order = append(r.order, name)
}

// Get returns the named algorithm.
func (r *Registry) Get(name string) (Algorithm, bool) {
	a, ok := r.algos[name]
	return a, ok
}

// Describe returns descriptors for every algorithm in registration order.
func (r *Registry) Describe() []Descriptor {
	out := make([]Descriptor, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.algos[n].Describe())
	}
	return out
}
