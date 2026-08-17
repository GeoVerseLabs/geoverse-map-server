package config

import "testing"

func TestDataSourcePolicyCheckTableUnconfigured(t *testing.T) {
	var p DataSourcePolicy
	if p.Configured() {
		t.Fatal("empty policy must report Configured() = false")
	}
	if err := p.CheckTable("anything", "goes"); err != nil {
		t.Fatalf("unconfigured policy must allow any table, got %v", err)
	}
}

func TestDataSourcePolicyAllowedSchemas(t *testing.T) {
	p := DataSourcePolicy{AllowedSchemas: []string{"geo", "Public"}}
	if !p.Configured() {
		t.Fatal("Configured() must be true once allowed_schemas is set")
	}
	if err := p.CheckTable("geo", "feature"); err != nil {
		t.Fatalf("geo.feature should be allowed: %v", err)
	}
	// Case-insensitive: PostGIS folds unquoted identifiers to lowercase and
	// MySQL's case sensitivity is platform-dependent.
	if err := p.CheckTable("public", "whatever"); err != nil {
		t.Fatalf("public.whatever should be allowed case-insensitively: %v", err)
	}
	if err := p.CheckTable("secrets", "table"); err == nil {
		t.Fatal("secrets.table should be rejected: not in allowed_schemas")
	}
}

func TestDataSourcePolicyAllowedTables(t *testing.T) {
	p := DataSourcePolicy{AllowedTables: []string{"geo.feature", "geoverse_demo.warehouse"}}
	if err := p.CheckTable("geo", "feature"); err != nil {
		t.Fatalf("geo.feature should be allowed: %v", err)
	}
	if err := p.CheckTable("GEO", "FEATURE"); err != nil {
		t.Fatalf("case-insensitive match should allow GEO.FEATURE: %v", err)
	}
	if err := p.CheckTable("geo", "other_table"); err == nil {
		t.Fatal("geo.other_table should be rejected: not in allowed_tables")
	}
}

func TestDataSourcePolicyBothDimensions(t *testing.T) {
	// allowed_schemas narrows which schemas are even eligible; allowed_tables
	// then narrows further within those schemas. Both must pass.
	p := DataSourcePolicy{
		AllowedSchemas: []string{"geo"},
		AllowedTables:  []string{"geo.feature"},
	}
	if err := p.CheckTable("geo", "feature"); err != nil {
		t.Fatalf("geo.feature should satisfy both dimensions: %v", err)
	}
	if err := p.CheckTable("geo", "other"); err == nil {
		t.Fatal("geo.other passes allowed_schemas but must fail allowed_tables")
	}
}
