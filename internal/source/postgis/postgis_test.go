package postgis

import "testing"

func TestIsPostgresInteger(t *testing.T) {
	for _, typ := range []string{"int2", "int4", "int8"} {
		if !isPostgresInteger(typ) {
			t.Errorf("%q should be a valid MVT feature id type", typ)
		}
	}
	for _, typ := range []string{"uuid", "text", "numeric"} {
		if isPostgresInteger(typ) {
			t.Errorf("%q must not be used as an MVT feature id type", typ)
		}
	}
}

func TestNormalizeUUID(t *testing.T) {
	raw := [16]byte{
		0xf1, 0xea, 0xc8, 0x33, 0xf9, 0x73, 0x4c, 0x81,
		0x83, 0x6d, 0x57, 0xca, 0xe5, 0x64, 0x61, 0x51,
	}
	if got, want := normalizeJSON(raw), "f1eac833-f973-4c81-836d-57cae5646151"; got != want {
		t.Fatalf("normalizeJSON(UUID) = %v, want %s", got, want)
	}
}
