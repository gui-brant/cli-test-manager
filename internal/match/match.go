// Package match ties results that came from outside (a pulled CI file, an
// imported result file) to the tests in the manifest. Pulling and importing
// share it, so they can never disagree about which test a result belongs to.
//
// It is pure: names and results in, results out.
package match

import (
	"fmt"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
)

// Finder returns the manifest test a group name belongs to, and false when it
// belongs to none.
type Finder func(group string) (core.TestSpec, bool)

// Tests finds the manifest test a result belongs to, from the suite or class
// name its file printed it under (entry 56 of the design):
//
//  1. a name equal to a test's id matches that test;
//  2. otherwise a name equal to a test's file path, or ending with it after a
//     slash (the workflow may print a longer path), matches that test. When
//     several paths match, the longest wins.
//
// Anything else matches no test. Such a result is kept, without a test, and
// flagged, never dropped.
func Tests(m *core.Manifest) Finder {
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

// Only returns a Finder that gives every result to one test, whatever group it
// was printed under. It is for a file the caller says is about a single test.
func Only(t core.TestSpec) Finder {
	return func(string) (core.TestSpec, bool) { return t, true }
}

// normalizePath makes two spellings of one path equal: slashes, no leading ./.
func normalizePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return p
}

// Results turns parsed items into results, giving each the test (and the
// feature, as the manifest has it now) that find returns. It also gathers the
// tool's own detail on failures, as a local run does, and counts the results
// that matched no test.
func Results(items []parse.Item, find Finder) (results []core.Result, detail string, unmatched int) {
	var details []string
	for _, it := range items {
		r := it.Result
		if t, ok := find(it.Group); ok {
			r.TestID, r.Feature = t.ID, t.Feature // the feature is recorded as it is now
		} else {
			unmatched++
		}
		results = append(results, r)
		if r.Status.Failed() && strings.TrimSpace(it.Detail) != "" {
			label := r.TestID
			if label == "" {
				label = it.Group
			}
			details = append(details, fmt.Sprintf("[%s] %s\n%s", label, r.Name, it.Detail))
		}
	}
	return results, core.TailDiagnostic(strings.TrimRight(strings.Join(details, "\n\n"), "\n")), unmatched
}
