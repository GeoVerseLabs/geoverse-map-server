package network

import (
	"context"
	"fmt"
	"sync"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/source"
)

// maxNetworkFeatures bounds how many features are pulled from a source
// when building a graph, protecting against runaway PostGIS tables.
const maxNetworkFeatures = 500000

// NetworkConfig describes one routable network (mirrors the `networks`
// section of the YAML config; duplicated here to avoid an import cycle).
type NetworkConfig struct {
	Name            string
	Source          string // feature source the linework comes from
	DefaultSpeedKMH float64
	SpeedField      string
	OnewayField     string
	LevelField      string
	NameField       string
}

// Manager lazily builds and caches one Graph per configured network.
// Graphs are immutable once built, so a build happens at most once.
type Manager struct {
	configs map[string]NetworkConfig
	order   []string
	lookup  func(name string) (source.FeatureSource, bool)

	mu     sync.Mutex
	graphs map[string]*Graph
	errs   map[string]error
}

// NewManager creates a Manager over the given network configs. lookup
// resolves feature sources by name.
func NewManager(configs []NetworkConfig, lookup func(string) (source.FeatureSource, bool)) *Manager {
	m := &Manager{
		configs: map[string]NetworkConfig{},
		lookup:  lookup,
		graphs:  map[string]*Graph{},
		errs:    map[string]error{},
	}
	for _, c := range configs {
		m.configs[c.Name] = c
		m.order = append(m.order, c.Name)
	}
	return m
}

// Names returns configured network names in order.
func (m *Manager) Names() []string {
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// Graph returns the built graph for the named network, constructing it on
// first use. Build failures are cached and returned on later calls.
func (m *Manager) Graph(ctx context.Context, name string) (*Graph, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.graphs[name]; ok {
		return g, nil
	}
	if err, ok := m.errs[name]; ok {
		return nil, err
	}
	cfg, ok := m.configs[name]
	if !ok {
		return nil, fmt.Errorf("unknown network %q (configured: %v)", name, m.order)
	}
	g, err := m.build(ctx, cfg)
	if err != nil {
		m.errs[name] = err
		return nil, err
	}
	m.graphs[name] = g
	return g, nil
}

func (m *Manager) build(ctx context.Context, cfg NetworkConfig) (*Graph, error) {
	fs, ok := m.lookup(cfg.Source)
	if !ok {
		return nil, fmt.Errorf("network %q: source %q is not a feature source", cfg.Name, cfg.Source)
	}
	res, err := fs.Features(ctx, source.FeatureQuery{Limit: maxNetworkFeatures})
	if err != nil {
		return nil, fmt.Errorf("network %q: load features: %w", cfg.Name, err)
	}
	g, err := Build(res.Features, BuildOptions{
		DefaultSpeedKMH: cfg.DefaultSpeedKMH,
		SpeedField:      cfg.SpeedField,
		OnewayField:     cfg.OnewayField,
		LevelField:      cfg.LevelField,
		NameField:       cfg.NameField,
	})
	if err != nil {
		return nil, fmt.Errorf("network %q: %w", cfg.Name, err)
	}
	return g, nil
}
