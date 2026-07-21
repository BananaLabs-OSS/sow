package sow

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Manifest is an explicit, shippable migration plan. Entries are paths relative
// to the manifest file and run in the authored order; the filename still supplies
// each migration's stable version and name.
type Manifest struct {
	Migrations []string `json:"migrations"`
}

// LoadManifestFS loads migrations in the exact order declared by a JSON manifest.
// It works with embed.FS, os.DirFS, or any other fs.FS, so an application can ship
// its migration plan and SQL together without hard-coding schema steps in Go.
func LoadManifestFS(fsys fs.FS, manifestPath string) ([]Migration, error) {
	data, err := fs.ReadFile(fsys, manifestPath)
	if err != nil {
		return nil, fmt.Errorf("sow: read manifest %q: %w", manifestPath, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("sow: parse manifest %q: %w", manifestPath, err)
	}
	if len(manifest.Migrations) == 0 {
		return nil, fmt.Errorf("sow: manifest %q has no migrations", manifestPath)
	}
	dir := path.Dir(manifestPath)
	seenPath := map[string]bool{}
	seenVersion := map[string]string{}
	out := make([]Migration, 0, len(manifest.Migrations))
	for i, entry := range manifest.Migrations {
		entry = strings.TrimSpace(entry)
		if entry == "" || path.IsAbs(entry) || strings.HasPrefix(path.Clean(entry), "../") {
			return nil, fmt.Errorf("sow: manifest %q entry %d is not a relative migration path", manifestPath, i+1)
		}
		migrationPath := path.Clean(path.Join(dir, entry))
		if seenPath[migrationPath] {
			return nil, fmt.Errorf("sow: manifest %q repeats %q", manifestPath, entry)
		}
		seenPath[migrationPath] = true
		if path.Ext(migrationPath) != ".sql" {
			return nil, fmt.Errorf("sow: manifest %q entry %q is not a .sql migration", manifestPath, entry)
		}
		migrationData, err := fs.ReadFile(fsys, migrationPath)
		if err != nil {
			return nil, fmt.Errorf("sow: read manifest migration %q: %w", migrationPath, err)
		}
		m := Parse(path.Base(migrationPath), string(migrationData))
		m.Path = migrationPath
		if previous, ok := seenVersion[m.Version]; ok {
			return nil, fmt.Errorf("sow: duplicate migration version %q (%s and %s)", m.Version, previous, migrationPath)
		}
		seenVersion[m.Version] = migrationPath
		out = append(out, m)
	}
	return out, nil
}

// LoadFS reads every *.sql migration under dir in an fs.FS — an embed.FS in a wasm cell,
// os.DirFS on disk, or any other fs.FS — parses them, and returns them sorted by version.
// This is the driver-agnostic sibling of Load(dir): it uses only io/fs, so it works inside
// a Pulp cell where there is no os filesystem and migrations ship embedded in the binary.
func LoadFS(fsys fs.FS, dir string) ([]Migration, error) {
	glob := "*.sql"
	if d := path.Clean(dir); d != "" && d != "." {
		glob = d + "/*.sql"
	}
	matches, err := fs.Glob(fsys, glob)
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, p := range matches {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, fmt.Errorf("sow: read %q: %w", p, err)
		}
		m := Parse(path.Base(p), string(data))
		m.Path = p
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("sow: duplicate migration version %q (%s and %s)",
				out[i].Version, out[i-1].Path, out[i].Path)
		}
	}
	return out, nil
}
