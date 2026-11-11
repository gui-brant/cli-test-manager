// Package storetest is a contract test suite for core.Store. Any store, the
// SQLite one or a fake, must pass it, so they all behave the same. A store's
// own tests call Run with a function that opens a fresh, empty store.
package storetest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// Open returns a fresh, empty store for one test. The suite closes it when the
// test needs a closed store; otherwise the caller's cleanup does.
type Open func(t *testing.T) core.Store

// Run runs the whole contract suite against stores made by open.
func Run(t *testing.T, open Open) {
	cases := []struct {
		name string
		fn   func(*testing.T, core.Store)
	}{
		{"a saved run round-trips every field", testRoundTrip},
		{"local runs are always new rows", testLocalRunsAreDistinct},
		{"a keyed run replaces itself and keeps its ID", testKeyedRuns},
		{"missing runs are ErrNotFound", testNotFound},
		{"listing is newest first and pages", testListOrderAndPaging},
		{"listing filters", testListFilters},
		{"summaries carry the stored verdict and counts", testSummaryFields},
		{"test stats", testTestStats},
		{"large and unusual text round-trips", testLargeAndUnicode},
		{"a cancelled context is an error", testCancelledContext},
		{"a closed store is an error", testClosedStore},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, open(t)) })
	}
}

var (
	ctx = context.Background()
	t0  = time.Date(2026, 11, 10, 12, 0, 0, 123456789, time.UTC)
)

func result(testID, feature, name string, st core.Status) core.Result {
	return core.Result{TestID: testID, Feature: feature, Name: name, Status: st, Duration: 1500 * time.Millisecond}
}

func local(env string, started time.Time, results ...core.Result) core.Run {
	return core.Run{
		Source: core.SourceLocal, Environment: env, State: core.RunComplete,
		StartedAt: started, FinishedAt: started.Add(2 * time.Second), Results: results,
	}
}

func remote(id string, attempt int, state core.RunState, started time.Time, results ...core.Result) core.Run {
	return core.Run{
		Source: core.SourceRemote, Environment: "deployed", State: state,
		StartedAt: started, FinishedAt: started.Add(time.Minute),
		ExternalID: id, Attempt: attempt, CommitSHA: "abc123", Results: results,
	}
}

func save(t *testing.T, s core.Store, r core.Run) int64 {
	t.Helper()
	id, err := s.SaveRun(ctx, r)
	if err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if id == 0 {
		t.Fatal("SaveRun returned the ID 0")
	}
	return id
}

func ids(sums []core.RunSummary) []int64 {
	out := []int64{}
	for _, s := range sums {
		out = append(out, s.ID)
	}
	return out
}

// sameRun compares everything but the ID, which the store assigns. Times are
// compared as instants, not as Go values.
func sameRun(t *testing.T, got, want core.Run) {
	t.Helper()
	if got.Source != want.Source || got.Environment != want.Environment || got.State != want.State ||
		got.ExitCode != want.ExitCode || got.ExternalID != want.ExternalID || got.Attempt != want.Attempt ||
		got.CommitSHA != want.CommitSHA || got.Diagnostic != want.Diagnostic || got.Detail != want.Detail {
		t.Errorf("run fields differ:\n got  %+v\n want %+v", got, want)
	}
	if !got.StartedAt.Equal(want.StartedAt) || !got.FinishedAt.Equal(want.FinishedAt) {
		t.Errorf("times differ: got %v to %v; want %v to %v", got.StartedAt, got.FinishedAt, want.StartedAt, want.FinishedAt)
	}
	if len(got.Results) != len(want.Results) {
		t.Fatalf("got %d results; want %d", len(got.Results), len(want.Results))
	}
	for i := range want.Results {
		if !reflect.DeepEqual(got.Results[i], want.Results[i]) {
			t.Errorf("result %d differs:\n got  %+v\n want %+v", i, got.Results[i], want.Results[i])
		}
	}
}

func testRoundTrip(t *testing.T, s core.Store) {
	want := core.Run{
		Source: core.SourceRemote, Environment: "deployed", State: core.RunComplete,
		StartedAt: t0, FinishedAt: t0.Add(90*time.Second + 7*time.Nanosecond),
		ExternalID: "9001", Attempt: 2, CommitSHA: "deadbeef",
		Diagnostic: "tool: could not connect\nerror: refused",
		Detail:     "[login-flow] rejects\n  at login.js:10",
		Results: []core.Result{
			{TestID: "login-flow", Feature: "accounts", Name: "signs in", Status: core.StatusPass, Duration: 250 * time.Millisecond},
			{TestID: "login-flow", Feature: "accounts", Name: "rejects ação — 日本語", Status: core.StatusFail, Duration: 1500 * time.Millisecond, Message: "expected 401\ngot 200"},
			{Name: "an unlisted thing", Status: core.StatusSkip, Message: "no network"},
			{TestID: "x", Feature: "general", Name: "boom", Status: core.StatusError, Message: strings.Repeat("e", 2048)},
		},
	}
	id := save(t, s, want)

	got, err := s.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Errorf("GetRun returned ID %d; want %d", got.ID, id)
	}
	sameRun(t, got, want)

	// Order is part of the contract: results come back as they were saved.
	for i, r := range got.Results {
		if r.Name != want.Results[i].Name {
			t.Errorf("result %d is %q; want %q", i, r.Name, want.Results[i].Name)
		}
	}
}

func testLocalRunsAreDistinct(t *testing.T, s core.Store) {
	run := local("local", t0, result("a", "f", "A", core.StatusPass))
	id1, id2 := save(t, s, run), save(t, s, run)
	if id1 == id2 {
		t.Errorf("two local runs share the ID %d; a run with no key must always be a new row", id1)
	}
	if got, _ := s.ListRuns(ctx, core.RunFilter{}); len(got) != 2 {
		t.Errorf("listed %d runs; want 2", len(got))
	}
}

func testKeyedRuns(t *testing.T, s core.Store) {
	// A pending placeholder, later completed.
	id1 := save(t, s, remote("9001", 1, core.RunPending, t0))
	two := []core.Result{result("a", "f", "A", core.StatusPass), result("b", "f", "B", core.StatusFail)}
	id2 := save(t, s, remote("9001", 1, core.RunComplete, t0, two...))
	if id1 != id2 {
		t.Fatalf("completing a pending run changed its ID from %d to %d", id1, id2)
	}
	got, err := s.GetRun(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.RunComplete || len(got.Results) != 2 {
		t.Errorf("after completing: state %q with %d results; want complete with 2", got.State, len(got.Results))
	}

	// Saving again with fewer results must not leave the old ones behind.
	save(t, s, remote("9001", 1, core.RunComplete, t0, two[0]))
	got, _ = s.GetRun(ctx, id1)
	if len(got.Results) != 1 {
		t.Errorf("after re-saving with one result, the run has %d; the old results must be replaced", len(got.Results))
	}
	if all, _ := s.ListRuns(ctx, core.RunFilter{}); len(all) != 1 {
		t.Errorf("listed %d runs; want 1 (one key, one row)", len(all))
	}

	// A re-run keeps the external id and raises the attempt: a different run.
	if id3 := save(t, s, remote("9001", 2, core.RunComplete, t0.Add(time.Hour), two[0])); id3 == id1 {
		t.Error("attempt 2 replaced attempt 1; they are different runs")
	}

	found, err := s.FindByKey(ctx, "9001", 1)
	if err != nil || found.ID != id1 {
		t.Errorf("FindByKey(9001, 1) = ID %d, %v; want %d", found.ID, err, id1)
	}
}

func testNotFound(t *testing.T, s core.Store) {
	if _, err := s.GetRun(ctx, 424242); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("GetRun of a missing ID: %v; want core.ErrNotFound", err)
	}
	if _, err := s.FindByKey(ctx, "nope", 1); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("FindByKey of a missing key: %v; want core.ErrNotFound", err)
	}
	save(t, s, remote("9001", 1, core.RunComplete, t0))
	if _, err := s.FindByKey(ctx, "9001", 2); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("FindByKey of a missing attempt: %v; want core.ErrNotFound", err)
	}
	if _, err := s.FindByKey(ctx, "", 0); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("FindByKey with no external id: %v; want core.ErrNotFound", err)
	}
}

func testListOrderAndPaging(t *testing.T, s core.Store) {
	saved := make([]int64, 55)
	for i := range saved {
		saved[i] = save(t, s, local("local", t0.Add(time.Duration(i)*time.Minute), result("a", "f", "A", core.StatusPass)))
	}

	first, err := s.ListRuns(ctx, core.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != core.DefaultListLimit {
		t.Fatalf("default listing returned %d runs; want %d", len(first), core.DefaultListLimit)
	}
	if first[0].ID != saved[54] || first[49].ID != saved[5] {
		t.Errorf("listing is not newest first: first %d, last %d; want %d, %d", first[0].ID, first[49].ID, saved[54], saved[5])
	}

	rest, _ := s.ListRuns(ctx, core.RunFilter{Offset: 50})
	if want := []int64{saved[4], saved[3], saved[2], saved[1], saved[0]}; !reflect.DeepEqual(ids(rest), want) {
		t.Errorf("offset 50 gave %v; want %v", ids(rest), want)
	}
	if three, _ := s.ListRuns(ctx, core.RunFilter{Limit: 3}); len(three) != 3 {
		t.Errorf("limit 3 gave %d runs", len(three))
	}
	if beyond, err := s.ListRuns(ctx, core.RunFilter{Offset: 500}); err != nil || len(beyond) != 0 {
		t.Errorf("offset past the end gave %d runs, %v; want none and no error", len(beyond), err)
	}
}

func testListFilters(t *testing.T, s core.Store) {
	a := save(t, s, local("local", t0, result("u1", "accounts", "U1", core.StatusPass)))
	b := save(t, s, local("local", t0.Add(time.Minute), result("n1", "notes", "N1", core.StatusFail)))
	c := save(t, s, remote("9001", 1, core.RunComplete, t0.Add(2*time.Minute), result("u2", "accounts", "U2", core.StatusPass)))
	d := save(t, s, remote("9002", 1, core.RunPending, t0.Add(3*time.Minute)))

	cases := []struct {
		name   string
		filter core.RunFilter
		want   []int64 // newest first
	}{
		{"no filter", core.RunFilter{}, []int64{d, c, b, a}},
		{"feature accounts", core.RunFilter{Feature: "accounts"}, []int64{c, a}},
		{"feature notes", core.RunFilter{Feature: "notes"}, []int64{b}},
		{"environment deployed", core.RunFilter{Environment: "deployed"}, []int64{d, c}},
		{"source remote", core.RunFilter{Source: core.SourceRemote}, []int64{d, c}},
		{"source local", core.RunFilter{Source: core.SourceLocal}, []int64{b, a}},
		{"state pending", core.RunFilter{State: core.RunPending}, []int64{d}},
		{"test u2", core.RunFilter{TestID: "u2"}, []int64{c}},
		{"feature and source combined", core.RunFilter{Feature: "accounts", Source: core.SourceRemote}, []int64{c}},
		{"nothing matches", core.RunFilter{Feature: "billing"}, []int64{}},
	}
	for _, c := range cases {
		got, err := s.ListRuns(ctx, c.filter)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(ids(got), c.want) {
			t.Errorf("%s: got %v; want %v", c.name, ids(got), c.want)
		}
	}
}

func testSummaryFields(t *testing.T, s core.Store) {
	pass := local("local", t0, result("b", "notes", "B", core.StatusPass), result("a", "accounts", "A", core.StatusPass))
	fail := local("local", t0.Add(time.Minute), result("a", "accounts", "A", core.StatusPass), result("b", "notes", "B", core.StatusFail))
	crashed := local("local", t0.Add(2*time.Minute))
	crashed.ExitCode = 2
	crashed.Diagnostic = "tool started\npanic: boom"
	pending := remote("9001", 3, core.RunPending, t0.Add(3*time.Minute))

	idPass, idFail, idCrash, idPending := save(t, s, pass), save(t, s, fail), save(t, s, crashed), save(t, s, pending)

	sums, err := s.ListRuns(ctx, core.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[int64]core.RunSummary{}
	for _, sum := range sums {
		by[sum.ID] = sum
	}

	check := func(name string, id int64, run core.Run, total, failed int, features []string) {
		t.Helper()
		sum, ok := by[id]
		if !ok {
			t.Fatalf("%s: run %d is not listed", name, id)
		}
		// The summary carries the verdict the core's rule gave at save time.
		if v := run.Verdict(); sum.Outcome != v.Outcome || sum.Reason != v.Reason {
			t.Errorf("%s: outcome %q reason %q; want %q %q", name, sum.Outcome, sum.Reason, v.Outcome, v.Reason)
		}
		if sum.Total != total || sum.Failed != failed {
			t.Errorf("%s: total %d failed %d; want %d %d", name, sum.Total, sum.Failed, total, failed)
		}
		if !reflect.DeepEqual(sum.Features, features) {
			t.Errorf("%s: features %v; want %v (sorted, distinct)", name, sum.Features, features)
		}
		if sum.Source != run.Source || sum.Environment != run.Environment || sum.State != run.State ||
			sum.ExternalID != run.ExternalID || sum.Attempt != run.Attempt || !sum.StartedAt.Equal(run.StartedAt) {
			t.Errorf("%s: header fields differ: %+v", name, sum)
		}
	}
	check("pass", idPass, pass, 2, 0, []string{"accounts", "notes"})
	check("fail", idFail, fail, 2, 1, []string{"accounts", "notes"})
	check("crash", idCrash, crashed, 0, 0, []string{})
	check("pending", idPending, pending, 0, 0, []string{})

	if got := by[idCrash].Reason; got != "panic: boom" {
		t.Errorf("a crashed run's reason is %q; want the last line of its diagnostic", got)
	}
	if got := by[idPending].Outcome; got != core.OutcomeUndetermined {
		t.Errorf("a pending run's outcome is %q; want undetermined", got)
	}
}

func testTestStats(t *testing.T, s core.Store) {
	save(t, s, local("local", t0,
		result("t-a", "accounts", "Old name", core.StatusPass),
		result("t-b", "notes", "B", core.StatusFail),
		core.Result{Name: "unlisted", Status: core.StatusFail}))
	save(t, s, local("local", t0.Add(time.Hour),
		result("t-a", "accounts", "Old name", core.StatusFail)))
	save(t, s, local("deployed", t0.Add(2*time.Hour),
		result("t-a", "general", "New name", core.StatusPass),
		result("t-b", "notes", "B", core.StatusSkip),
		result("t-c", "notes", "C", core.StatusPass)))

	byID := func(stats []core.TestStat) map[string]core.TestStat {
		m := map[string]core.TestStat{}
		for _, st := range stats {
			m[st.TestID] = st
		}
		return m
	}

	all, err := s.TestStats(ctx, core.StatsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].TestID != "t-a" || all[1].TestID != "t-b" || all[2].TestID != "t-c" {
		t.Fatalf("stats are %v; want t-a, t-b, t-c in order, with the unlisted result left out", all)
	}
	a := all[0]
	if a.Runs != 3 || a.Passed != 2 || a.Failed != 1 || a.Errored != 0 || a.Skipped != 0 {
		t.Errorf("t-a counts: %+v", a)
	}
	// Name, feature and last status come from the most recent result.
	if a.Name != "New name" || a.Feature != "general" || a.LastStatus != core.StatusPass || !a.LastRunAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("t-a latest: name %q feature %q status %q at %v", a.Name, a.Feature, a.LastStatus, a.LastRunAt)
	}
	if b := all[1]; b.Runs != 2 || b.Failed != 1 || b.Skipped != 1 || b.LastStatus != core.StatusSkip {
		t.Errorf("t-b: %+v", b)
	}

	local, _ := s.TestStats(ctx, core.StatsFilter{Environment: "local"})
	lm := byID(local)
	if _, ok := lm["t-c"]; ok || len(local) != 2 {
		t.Errorf("environment local still lists t-c: %v", local)
	}
	if a := lm["t-a"]; a.Runs != 2 || a.Passed != 1 || a.Failed != 1 || a.Name != "Old name" || a.Feature != "accounts" || a.LastStatus != core.StatusFail {
		t.Errorf("t-a in local: %+v", a)
	}

	notes, _ := s.TestStats(ctx, core.StatsFilter{Feature: "notes"})
	nm := byID(notes)
	if _, ok := nm["t-a"]; ok || len(notes) != 2 {
		t.Errorf("feature notes lists %v; want only t-b and t-c", notes)
	}

	since, _ := s.TestStats(ctx, core.StatsFilter{Since: t0.Add(90 * time.Minute)})
	if len(since) != 3 || since[0].Runs != 1 || since[1].Runs != 1 || since[2].Runs != 1 {
		t.Errorf("since 90 minutes: %v; want the three tests with one run each", since)
	}
}

func testLargeAndUnicode(t *testing.T, s core.Store) {
	run := local("local", t0, core.Result{TestID: "big", Feature: "f", Name: "日本語 ação", Status: core.StatusFail, Message: strings.Repeat("日本", 4096)})
	run.Diagnostic = strings.Repeat("line of output\n", 2000)
	id := save(t, s, run)
	got, err := s.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sameRun(t, got, run)
}

func testCancelledContext(t *testing.T, s core.Store) {
	id := save(t, s, local("local", t0, result("a", "f", "A", core.StatusPass)))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	calls := map[string]func() error{
		"SaveRun":   func() error { _, err := s.SaveRun(cancelled, local("local", t0)); return err },
		"GetRun":    func() error { _, err := s.GetRun(cancelled, id); return err },
		"FindByKey": func() error { _, err := s.FindByKey(cancelled, "9001", 1); return err },
		"ListRuns":  func() error { _, err := s.ListRuns(cancelled, core.RunFilter{}); return err },
		"TestStats": func() error { _, err := s.TestStats(cancelled, core.StatsFilter{}); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s with a cancelled context returned no error", name)
		} else if errors.Is(err, core.ErrNotFound) {
			t.Errorf("%s: a cancelled context must not look like a missing run: %v", name, err)
		}
	}

	// The cancelled save must not have written anything.
	if got, _ := s.ListRuns(ctx, core.RunFilter{}); len(got) != 1 {
		t.Errorf("%d runs after a cancelled save; want 1", len(got))
	}
}

func testClosedStore(t *testing.T, s core.Store) {
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.SaveRun(ctx, local("local", t0)); err == nil {
		t.Error("SaveRun on a closed store returned no error")
	}
	if _, err := s.ListRuns(ctx, core.RunFilter{}); err == nil {
		t.Error("ListRuns on a closed store returned no error")
	}
}
