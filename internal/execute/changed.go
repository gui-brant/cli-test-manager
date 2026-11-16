package execute

import (
	"path"
	"sort"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/match"
)

// ChangedSummary says how a set of changed files mapped to features, so a run
// can show what it chose and why.
type ChangedSummary struct {
	// Files is how many distinct changed paths inside the project were looked at.
	Files int
	// Outside is how many paths were left out because they are not inside the
	// project (absolute, or starting with ..).
	Outside int
	// InNoFeature is how many files matched no feature and fell under `general`.
	InNoFeature int
	// Manifest is true when the manifest itself was among the changes, which
	// selects every feature.
	Manifest bool
	// Features are the features the changes select, sorted.
	Features []string
}

// featuresOfChanges maps changed files to the features they select:
//
//   - the manifest itself selects every feature, since a change to it can
//     affect any test;
//   - otherwise a file selects every feature whose path patterns match it, and
//     the feature of any test whose own file it is (so a pattern list that
//     forgets the tests cannot make a changed test be missed);
//   - a file that does none of that falls under `general`.
//
// Paths are relative to the project, with slashes. manifestFile is the
// manifest's own path (empty means DefaultManifestFile).
func featuresOfChanges(m *core.Manifest, manifestFile string, changed []string) (map[string]bool, ChangedSummary) {
	if manifestFile == "" {
		manifestFile = DefaultManifestFile
	}
	testFeature := make(map[string]string, len(m.Tests))
	for _, t := range m.Tests {
		testFeature[normalize(t.File)] = t.Feature
	}

	selected := make(map[string]bool)
	var sum ChangedSummary
	seen := make(map[string]bool)
	for _, raw := range changed {
		p, ok := cleanChanged(raw)
		if !ok {
			if strings.TrimSpace(raw) != "" {
				sum.Outside++
			}
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		sum.Files++

		if p == manifestFile {
			sum.Manifest = true
			selected[core.GeneralFeature] = true
			for _, name := range m.FeatureNames() {
				selected[name] = true
			}
			continue
		}
		matched := false
		for name, spec := range m.Features {
			if match.GlobAny(spec.Paths, p) {
				selected[name], matched = true, true
			}
		}
		if f, ok := testFeature[p]; ok {
			selected[f], matched = true, true
		}
		if !matched {
			selected[core.GeneralFeature] = true
			sum.InNoFeature++
		}
	}
	for f := range selected {
		sum.Features = append(sum.Features, f)
	}
	sort.Strings(sum.Features)
	return selected, sum
}

// cleanChanged turns a changed path into a clean relative slash path. ok is
// false for an empty line and for a path that is not inside the project.
func cleanChanged(raw string) (string, bool) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if p == "" {
		return "", false
	}
	if path.IsAbs(p) {
		return "", false
	}
	p = path.Clean(p)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// normalize makes a manifest path comparable with a changed one.
func normalize(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return path.Clean(p)
}
