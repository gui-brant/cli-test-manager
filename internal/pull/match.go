package pull

import (
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// matcher finds the manifest test a pulled result belongs to, from the suite or
// class name its JUnit file printed it under (entry 56 of the design):
//
//  1. a name equal to a test's id matches that test;
//  2. otherwise a name equal to a test's file path, or ending with it after a
//     slash (the workflow may print a longer path), matches that test. When
//     several paths match, the longest wins.
//
// Anything else matches no test. Such a result is kept, without a test, and
// flagged, never dropped.
func matcher(m *core.Manifest) func(group string) (core.TestSpec, bool) {
	byID := make(map[string]core.TestSpec, len(m.Tests))
	files := make([]core.TestSpec, 0, len(m.Tests))
	for _, t := range m.Tests {
		byID[t.ID] = t
		files = append(files, t)
	}
	return func(group string) (core.TestSpec, bool) {
		group = strings.TrimSpace(group)
		if group == "" {
			return core.TestSpec{}, false
		}
		if t, ok := byID[group]; ok {
			return t, true
		}
		g := normalizePath(group)
		best, found := core.TestSpec{}, false
		for _, t := range files {
			f := normalizePath(t.File)
			if f == "" {
				continue
			}
			if g == f || strings.HasSuffix(g, "/"+f) {
				if !found || len(f) > len(normalizePath(best.File)) {
					best, found = t, true
				}
			}
		}
		return best, found
	}
}

// normalizePath makes two spellings of one path equal: slashes, no leading ./.
func normalizePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return p
}
