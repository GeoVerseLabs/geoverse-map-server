package config

import (
	"fmt"
	"strings"
)

// DataSourcePolicy is the safety policy for database-backed data sources
// (postgis, mysql). Unlike Assets it never blocks a source that is already
// registered from being opened on the strength of the DSN account's own
// grants — CheckTable is a second, independent guard against
// misconfiguration (the wrong table got registered), not a privilege
// mechanism; the account's own GRANTs remain the real security boundary.
// Omitting AllowedSchemas/AllowedTables preserves legacy behaviour (any
// table the DSN account can read is registrable); doctor reports that as a
// compatibility-mode warning, mirroring Assets.
type DataSourcePolicy struct {
	AllowedSchemas []string `yaml:"allowed_schemas" json:"allowed_schemas,omitempty"`
	AllowedTables  []string `yaml:"allowed_tables" json:"allowed_tables,omitempty"`
	// RequireReadOnlyRole only affects `-doctor` output — it does not block
	// Open. When true, doctor asks each postgis/mysql source to report any
	// privilege beyond SELECT the configured account holds on the
	// configured table; a probe that cannot run at all (insufficient rights
	// to query the privilege catalog, common on managed databases even for
	// legitimate limited accounts) is reported informationally, not as a
	// warning.
	RequireReadOnlyRole bool `yaml:"require_readonly_role" json:"require_readonly_role,omitempty"`
}

// Configured reports whether an allowlist has been set at all, so callers
// can tell "no policy configured" (legacy/compatibility mode) apart from
// "policy configured and this table happens to satisfy it".
func (p DataSourcePolicy) Configured() bool {
	return len(p.AllowedSchemas) > 0 || len(p.AllowedTables) > 0
}

// CheckTable enforces the allowlist. Empty lists mean "no restriction" on
// that dimension. Comparison is case-insensitive: PostGIS folds unquoted
// identifiers to lowercase and MySQL's table name case sensitivity depends
// on the host OS / lower_case_table_names, so an operator typing the
// "obvious" case should not be tripped up by a platform-specific mismatch.
func (p DataSourcePolicy) CheckTable(schema, table string) error {
	if len(p.AllowedSchemas) > 0 && !containsFold(p.AllowedSchemas, schema) {
		return fmt.Errorf("schema %q is not in data_sources.allowed_schemas", schema)
	}
	if len(p.AllowedTables) > 0 {
		qualified := schema + "." + table
		if !containsFold(p.AllowedTables, qualified) {
			return fmt.Errorf("table %q is not in data_sources.allowed_tables", qualified)
		}
	}
	return nil
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
