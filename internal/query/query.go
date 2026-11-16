// Package query answers the questions the history screens ask: which runs
// exist, what one run holds, how each test has done, and which tests the
// manifest lists. It reads the store and the manifest and prints nothing; the
// commands decide how to show the answers.
//
// Tests are numbered by their position in the manifest, counting from 1. The
// test list is append-only, so a number stays with its test.
package query

import (
	"context"
	"fmt"
	"sort"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// Service reads history.
type Service struct {
	Store core.Store
}

// History lists runs, newest first, as the store does.
func (s *Service) History(ctx context.Context, f core.RunFilter) ([]core.RunSummary, error) {
	return s.Store.ListRuns(ctx, f)
}

// DetailResult is a stored result with its test's number.
type DetailResult struct {
	core.Result
	// Number is the test's position in the manifest, from 1. It is 0 when the
	// result belongs to no test, or to a test the manifest no longer lists.
	Number int
}

// RunDetail is one run in full.
type RunDetail struct {
	Run core.Run
	// Verdict is worked out from the stored run by the core's rule.
	Verdict core.Verdict
	Results []DetailResult
}

// Run returns one run with its results numbered, or core.ErrNotFound.
func (s *Service) Run(ctx context.Context, m *core.Manifest, id int64) (RunDetail, error) {
	run, err := s.Store.GetRun(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	numbers := Numbers(m)
	d := RunDetail{Run: run, Verdict: run.Verdict()}
	for _, r := range run.Results {
		d.Results = append(d.Results, DetailResult{Result: r, Number: numbers[r.TestID]})
	}
	return d, nil
}

// CountRow is one test's history with its number.
type CountRow struct {
	core.TestStat
	Number int // 0 for a test the manifest no longer lists
}

// Counts returns per-test counts. Tests the manifest lists come first, in
// manifest order; tests it no longer lists follow, by id.
//
// A listed test shows the manifest's name. A test file can print many results,
// each with a name of its own, so the name of its latest result would be
// arbitrary. A test the manifest no longer lists keeps the name last recorded.
func (s *Service) Counts(ctx context.Context, m *core.Manifest, f core.StatsFilter) ([]CountRow, error) {
	stats, err := s.Store.TestStats(ctx, f)
	if err != nil {
		return nil, err
	}
	numbers := Numbers(m)
	rows := make([]CountRow, 0, len(stats))
	for _, st := range stats {
		row := CountRow{TestStat: st, Number: numbers[st.TestID]}
		if row.Number > 0 {
			row.Name = m.Tests[row.Number-1].Name
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch {
		case a.Number != 0 && b.Number != 0:
			return a.Number < b.Number
		case a.Number != 0:
			return true
		case b.Number != 0:
			return false
		default:
			return a.TestID < b.TestID
		}
	})
	return rows, nil
}

// FeatureRow is one feature's history.
type FeatureRow struct {
	core.FeatureStat
	// Tests is how many tests the manifest lists in the feature.
	Tests int
}

// Features returns how often each feature's tests were played. Every feature
// the manifest lists comes first, by name, with zeros when it was never played
// (and `general` when some test belongs to it); features that appear in history
// but are no longer listed follow, by name.
func (s *Service) Features(ctx context.Context, m *core.Manifest, f core.StatsFilter) ([]FeatureRow, error) {
	stats, err := s.Store.FeatureStats(ctx, f)
	if err != nil {
		return nil, err
	}
	played := make(map[string]core.FeatureStat, len(stats))
	for _, st := range stats {
		played[st.Feature] = st
	}
	tests := make(map[string]int)
	for _, t := range m.Tests {
		tests[t.Feature]++
	}

	listed := m.FeatureNames()
	if _, explicit := m.Features[core.GeneralFeature]; !explicit && tests[core.GeneralFeature] > 0 {
		listed = append(listed, core.GeneralFeature)
		sort.Strings(listed)
	}
	var rows []FeatureRow
	for _, name := range listed {
		if f.Feature != "" && name != f.Feature {
			continue
		}
		st := played[name]
		st.Feature = name
		rows = append(rows, FeatureRow{FeatureStat: st, Tests: tests[name]})
		delete(played, name)
	}
	var rest []string
	for name := range played {
		rest = append(rest, name)
	}
	sort.Strings(rest)
	for _, name := range rest {
		rows = append(rows, FeatureRow{FeatureStat: played[name], Tests: tests[name]})
	}
	return rows, nil
}

// TestRow is a listed test with its number.
type TestRow struct {
	Number int
	core.TestSpec
}

// Tests lists the manifest's tests in order, numbered by their position in the
// whole list even when a feature filters it. An unknown feature is an error.
func Tests(m *core.Manifest, feature string) ([]TestRow, error) {
	if feature != "" && !m.HasFeature(feature) {
		return nil, fmt.Errorf("feature %q is not defined", feature)
	}
	var rows []TestRow
	for i, t := range m.Tests {
		if feature != "" && t.Feature != feature {
			continue
		}
		rows = append(rows, TestRow{Number: i + 1, TestSpec: t})
	}
	return rows, nil
}

// FindTest returns the test with the given id.
func FindTest(m *core.Manifest, id string) (TestRow, bool) {
	for i, t := range m.Tests {
		if t.ID == id {
			return TestRow{Number: i + 1, TestSpec: t}, true
		}
	}
	return TestRow{}, false
}

// Numbers maps each test id to its number.
func Numbers(m *core.Manifest) map[string]int {
	numbers := make(map[string]int)
	if m == nil {
		return numbers
	}
	for i, t := range m.Tests {
		numbers[t.ID] = i + 1
	}
	return numbers
}
