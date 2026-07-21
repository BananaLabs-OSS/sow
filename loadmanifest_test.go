package sow

import (
	"testing"
	"testing/fstest"
)

func TestLoadManifestFSPreservesAuthoredOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/manifest.json":  &fstest.MapFile{Data: []byte(`{"migrations":["002_second.sql","001_first.sql"]}`)},
		"migrations/001_first.sql":  &fstest.MapFile{Data: []byte("-- +sow up\nSELECT 1;")},
		"migrations/002_second.sql": &fstest.MapFile{Data: []byte("-- +sow up\nSELECT 2;")},
	}
	got, err := LoadManifestFS(fsys, "migrations/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != "002_second" || got[1].Version != "001_first" {
		t.Fatalf("manifest order not preserved: %#v", got)
	}
}

func TestLoadManifestFSRejectsInvalidPlans(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "empty", manifest: `{"migrations":[]}`},
		{name: "duplicate", manifest: `{"migrations":["001_a.sql","001_a.sql"]}`},
		{name: "escape", manifest: `{"migrations":["../001_a.sql"]}`},
		{name: "not sql", manifest: `{"migrations":["001_a.txt"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"migrations/manifest.json": &fstest.MapFile{Data: []byte(tt.manifest)},
				"migrations/001_a.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
				"migrations/001_a.txt":     &fstest.MapFile{Data: []byte("SELECT 1;")},
			}
			if _, err := LoadManifestFS(fsys, "migrations/manifest.json"); err == nil {
				t.Fatal("expected invalid manifest to fail")
			}
		})
	}
}
