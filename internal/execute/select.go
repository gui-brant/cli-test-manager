package execute

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// selectTests picks the tests to run, in manifest order, and the tests left
// out because their runner does not serve the environment. A test named by id
// that cannot run there is a problem, not a skip: it was asked for.
func selectTests(m *core.Manifest, envName string, sel Selection) (run []core.TestSpec, skipped []SkippedTest, problems []string) {
	explicit := make(map[string]bool)
	for _, id := range sel.TestIDs {
		t, ok := m.Test(id)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("test %q is not in the manifest", id))
		case sel.Feature != "" && t.Feature != sel.Feature:
			problems = append(problems, fmt.Sprintf("test %q belongs to feature %q, not %q", id, t.Feature, sel.Feature))
		default:
			explicit[id] = true
		}
	}
	if sel.Feature != "" && !m.HasFeature(sel.Feature) {
		problems = append(problems, fmt.Sprintf("feature %q is not defined", sel.Feature))
	}
	if len(problems) > 0 {
		return nil, nil, problems
	}

	for _, t := range m.Tests {
		if len(sel.TestIDs) > 0 && !explicit[t.ID] {
			continue
		}
		if sel.Feature != "" && t.Feature != sel.Feature {
			continue
		}
		r, ok := m.Runners[t.Runner]
		if !ok {
			problems = append(problems, fmt.Sprintf("test %q uses runner %q, which is not defined", t.ID, t.Runner))
			continue
		}
		if !slices.Contains(r.Environments, envName) {
			if explicit[t.ID] {
				problems = append(problems, fmt.Sprintf("test %q cannot run in environment %q: its runner %q runs only in %s",
					t.ID, envName, t.Runner, strings.Join(r.Environments, ", ")))
			} else {
				skipped = append(skipped, SkippedTest{Test: t, Reason: fmt.Sprintf("its runner %q runs only in: %s, not in %s",
					t.Runner, strings.Join(r.Environments, ", "), envName)})
			}
			continue
		}
		run = append(run, t)
	}

	if len(problems) == 0 && len(run) == 0 {
		if len(skipped) == 0 {
			return nil, nil, []string{"no tests match the selection"}
		}
		problems = append(problems, fmt.Sprintf("nothing to run: all %d selected tests were skipped", len(skipped)))
		for _, sk := range skipped {
			problems = append(problems, fmt.Sprintf("skipped test %q: %s", sk.Test.ID, sk.Reason))
		}
	}
	return run, skipped, problems
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
