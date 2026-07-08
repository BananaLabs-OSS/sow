package sew

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
)

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
			return nil, fmt.Errorf("sew: read %q: %w", p, err)
		}
		m := Parse(path.Base(p), string(data))
		m.Path = p
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("sew: duplicate migration version %q (%s and %s)",
				out[i].Version, out[i-1].Path, out[i].Path)
		}
	}
	return out, nil
}
