// Package storetest is a contract test suite for core.Store. Any store, the
// SQLite one or a fake, must pass it, so they all behave the same. A store's
// own tests call Run with a function that opens a fresh, empty store.
package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
		{"an imported run keeps its commit, dirty flag, trigger and tool versions", testRunMetadata},
		{"local runs are always new rows", testLocalRunsAreDistinct},
		{"a keyed run replaces itself and keeps its ID", testKeyedRuns},
		{"missing runs are ErrNotFound", testNotFound},
		{"listing is newest first and pages", testListOrderAndPaging},
		{"listing filters", testListFilters},
		{"summaries carry the stored verdict and counts", testSummaryFields},
		{"test stats", testTestStats},
		{"a test counts once per run, by the worst of its checks", testTestStatsCountOncePerRun},
		{"feature stats count runs, plays and failed plays", testFeatureStats},
		{"a run's files are kept as text and read back by path", testSnapshots},
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
		got.CommitSHA != want.CommitSHA || got.Diagnostic != want.Diagnostic || got.Detail != want.Detail ||
		got.Dirty != want.Dirty || got.Trigger != want.Trigger {
		t.Errorf("run fields differ:\n got  %+v\n want %+v", got, want)
	}
	if len(got.Tools) != len(want.Tools) || (len(want.Tools) > 0 && !reflect.DeepEqual(got.Tools, want.Tools)) {
		t.Errorf("tools differ:\n got  %+v\n want %+v", got.Tools, want.Tools)
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

// The metadata added for integration: where the run came from (including the
// import source), the commit and whether the checkout was dirty, what started
// it, and the tool versions in the order they were given.
func testRunMetadata(t *testing.T, s core.Store) {
	imported := core.Run{
		Source: core.SourceImport, Environment: "network", State: core.RunComplete,
		StartedAt: t0, FinishedAt: t0.Add(time.Minute), CommitSHA: "cafe1234",
		Tools:   []core.Tool{{Name: "k6", Version: "k6 v1.4.0"}, {Name: "pgbench", Version: "unknown: not given"}},
		Results: []core.Result{result("load-ramp", "general", "p95 under 450ms", core.StatusPass)},
	}
	got, err := s.GetRun(ctx, save(t, s, imported))
	if err != nil {
		t.Fatal(err)
	}
	sameRun(t, got, imported)

	hook := local("local", t0.Add(time.Hour), result("a", "f", "A", core.StatusPass))
	hook.CommitSHA, hook.Dirty, hook.Trigger = "abc", true, core.TriggerHook
	got, err = s.GetRun(ctx, save(t, s, hook))
	if err != nil {
		t.Fatal(err)
	}
	sameRun(t, got, hook)
	if !got.Dirty || got.Trigger != core.TriggerHook {
		t.Errorf("dirty %v, trigger %q; want true, hook", got.Dirty, got.Trigger)
	}

	// A plain run has none of it: no tools, not dirty, no trigger.
	plain := local("local", t0.Add(2*time.Hour), result("a", "f", "A", core.StatusPass))
	got, err = s.GetRun(ctx, save(t, s, plain))
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty || got.Trigger != "" || len(got.Tools) != 0 {
		t.Errorf("a plain run came back with dirty %v, trigger %q, tools %v", got.Dirty, got.Trigger, got.Tools)
	}

	// Replacing a keyed run replaces its tools too, not adds to them.
	r := remote("77", 1, core.RunComplete, t0, result("a", "f", "A", core.StatusPass))
	r.Tools = []core.Tool{{Name: "x", Version: "1"}, {Name: "y", Version: "2"}}
	id := save(t, s, r)
	r.Tools = []core.Tool{{Name: "z", Version: "3"}}
	if again := save(t, s, r); again != id {
		t.Fatalf("a keyed run got ID %d after %d", again, id)
	}
	if got, err = s.GetRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	sameRun(t, got, r)
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

func snapFile(path string, role core.SnapshotRole, testID, text string) core.SnapshotFile {
	sum := sha256.Sum256([]byte(text))
	return core.SnapshotFile{Path: path, Role: role, TestID: testID, Hash: hex.EncodeToString(sum[:]), Size: int64(len(text)), Content: []byte(text)}
}

// The files a run was made with are listed with the run, their text is read by
// path, a file whose text was not kept says why, and the latest snapshot of a
// test's file is the latest run's.
func testSnapshots(t *testing.T, s core.Store) {
	notKept := core.SnapshotFile{Path: "bin/tool", Role: core.RoleFeature, Hash: "ab12", Size: 9, Note: "binary"}

	first := local("local", t0, result("a", "f", "A", core.StatusPass))
	first.Files = []core.SnapshotFile{
		snapFile("tests/a.sql", core.RoleTest, "a", "select 1;"),
		snapFile("test-manifest.yaml", core.RoleManifest, "", "manifest one"),
		snapFile("db/m.sql", core.RoleFeature, "", "create table t();"),
		notKept,
	}
	id1 := save(t, s, first)

	got, err := s.GetRun(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	// Listed by path, with everything but the text.
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
		if f.Content != nil {
			t.Errorf("%s: a run read back carries text; it must be asked for by path", f.Path)
		}
	}
	if want := []string{"bin/tool", "db/m.sql", "test-manifest.yaml", "tests/a.sql"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("files = %v; want %v", paths, want)
	}
	if f := got.Files[3]; f.Role != core.RoleTest || f.TestID != "a" || f.Size != 9 || f.Hash != first.Files[0].Hash || !f.Kept() {
		t.Errorf("tests/a.sql = %+v", f)
	}
	if f := got.Files[0]; f.Kept() || f.Note != "binary" || f.Hash != "ab12" {
		t.Errorf("bin/tool = %+v; want it listed with its note and hash", f)
	}

	f, err := s.GetSnapshotFile(ctx, id1, "tests/a.sql")
	if err != nil || string(f.Content) != "select 1;" || f.Role != core.RoleTest || f.TestID != "a" {
		t.Errorf("GetSnapshotFile = %+v, %v", f, err)
	}
	if f, err := s.GetSnapshotFile(ctx, id1, "bin/tool"); err != nil || f.Content != nil || f.Note != "binary" {
		t.Errorf("a file whose text was not kept: %+v, %v; want its note and no text", f, err)
	}
	if _, err := s.GetSnapshotFile(ctx, id1, "nope.sql"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a path the run does not have: %v; want ErrNotFound", err)
	}
	if _, err := s.GetSnapshotFile(ctx, id1+999, "tests/a.sql"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a run that does not exist: %v; want ErrNotFound", err)
	}

	// A later run: the test's file changed, the manifest did not. Each run reads
	// its own text back.
	second := local("local", t0.Add(time.Hour), result("a", "f", "A", core.StatusPass))
	second.Files = []core.SnapshotFile{
		snapFile("tests/a.sql", core.RoleTest, "a", "select 2;"),
		snapFile("test-manifest.yaml", core.RoleManifest, "", "manifest one"),
	}
	id2 := save(t, s, second)
	for _, c := range []struct {
		run  int64
		path string
		want string
	}{{id1, "tests/a.sql", "select 1;"}, {id2, "tests/a.sql", "select 2;"}, {id1, "test-manifest.yaml", "manifest one"}, {id2, "test-manifest.yaml", "manifest one"}} {
		if f, err := s.GetSnapshotFile(ctx, c.run, c.path); err != nil || string(f.Content) != c.want {
			t.Errorf("run %d %s = %q, %v; want %q", c.run, c.path, f.Content, err, c.want)
		}
	}

	runID, latest, err := s.LatestTestFile(ctx, "a")
	if err != nil || runID != id2 || string(latest.Content) != "select 2;" {
		t.Errorf("LatestTestFile = run %d %q, %v; want run %d select 2;", runID, latest.Content, err, id2)
	}
	if _, _, err := s.LatestTestFile(ctx, "never-played"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a test no run has played: %v; want ErrNotFound", err)
	}

	// A keyed run replaces its files, it does not add to them.
	keyed := remote("5", 1, core.RunComplete, t0, result("a", "f", "A", core.StatusPass))
	keyed.Files = []core.SnapshotFile{snapFile("x.sql", core.RoleFeature, "", "one"), snapFile("y.sql", core.RoleFeature, "", "two")}
	kid := save(t, s, keyed)
	keyed.Files = []core.SnapshotFile{snapFile("z.sql", core.RoleFeature, "", "three")}
	if again := save(t, s, keyed); again != kid {
		t.Fatalf("a keyed run got ID %d after %d", again, kid)
	}
	if got, err := s.GetRun(ctx, kid); err != nil || len(got.Files) != 1 || got.Files[0].Path != "z.sql" {
		t.Errorf("after replacing: %+v, %v; want only z.sql", got.Files, err)
	}
}

// checks makes n results of one test with the given statuses.
func checks(testID, feature string, statuses ...core.Status) []core.Result {
	var out []core.Result
	for i, st := range statuses {
		out = append(out, result(testID, feature, fmt.Sprintf("%s check %d", testID, i+1), st))
	}
	return out
}

// A test file that prints many checks counts once per run. Its verdict in a run
// is the worst of its checks, and the four counts add up to the runs.
func testTestStatsCountOncePerRun(t *testing.T, s core.Store) {
	P, F, E, K := core.StatusPass, core.StatusFail, core.StatusError, core.StatusSkip
	runs := []struct {
		hours   time.Duration
		results []core.Result
	}{
		{0, checks("a", "f", P, P, P)}, // pass
		{1, append(checks("a", "f", P, P, P, P, F), result("", "", "unlisted", core.StatusFail))}, // fail; the unlisted result is ignored
		{2, checks("a", "f", K, K)},    // skip
		{3, checks("a", "f", F, E, P)}, // error beats fail
	}
	for _, r := range runs {
		save(t, s, local("local", t0.Add(r.hours*time.Hour), r.results...))
	}

	stats, err := s.TestStats(ctx, core.StatsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("%d tests; want only a (an unlisted result is not counted): %+v", len(stats), stats)
	}
	a := stats[0]
	if a.Runs != 4 || a.Passed != 1 || a.Failed != 1 || a.Errored != 1 || a.Skipped != 1 {
		t.Errorf("a: runs %d, pass %d, fail %d, error %d, skip %d; want 4 runs, 1 of each",
			a.Runs, a.Passed, a.Failed, a.Errored, a.Skipped)
	}
	if a.Passed+a.Failed+a.Errored+a.Skipped != a.Runs {
		t.Errorf("the counts do not add up to the runs: %+v", a)
	}
	if a.LastStatus != core.StatusError || !a.LastRunAt.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("last %q at %v; want error at the latest run", a.LastStatus, a.LastRunAt)
	}

	// A run's own totals count tests too.
	sums, err := s.ListRuns(ctx, core.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sum := range sums {
		wantTotal := 1
		if sum.StartedAt.Equal(t0.Add(1 * time.Hour)) {
			wantTotal = 2 // a, and the unlisted result as a play of its own
		}
		if sum.Total != wantTotal {
			t.Errorf("run at %v: total %d; want %d tests", sum.StartedAt, sum.Total, wantTotal)
		}
	}
}

func testFeatureStats(t *testing.T, s core.Store) {
	P, F := core.StatusPass, core.StatusFail
	save(t, s, local("local", t0, append(append(checks("a", "f1", P, P, P), checks("b", "f1", F)...), checks("c", "f2", P)...)...))
	save(t, s, local("deployed", t0.Add(2*time.Hour), checks("a", "f1", P)...))
	save(t, s, local("local", t0.Add(4*time.Hour), append(checks("d", "", P), result("", "", "unlisted", F))...)) // no feature, no test

	byFeature := func(stats []core.FeatureStat) map[string]core.FeatureStat {
		m := map[string]core.FeatureStat{}
		for _, st := range stats {
			m[st.Feature] = st
		}
		return m
	}
	all, err := s.FeatureStats(ctx, core.StatsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Feature != "f1" || all[1].Feature != "f2" {
		t.Fatalf("features %+v; want f1 then f2 (results with no feature are not counted)", all)
	}
	got := byFeature(all)
	// f1: a and b in run 1, a again in run 2 -> 2 runs, 3 plays, 1 failed. a's three checks are one play.
	if f1 := got["f1"]; f1.Runs != 2 || f1.Plays != 3 || f1.Failed != 1 || !f1.LastRunAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("f1 = %+v; want 2 runs, 3 plays, 1 failed, last at the second run", f1)
	}
	if f2 := got["f2"]; f2.Runs != 1 || f2.Plays != 1 || f2.Failed != 0 {
		t.Errorf("f2 = %+v; want 1 run, 1 play, 0 failed", f2)
	}

	local, _ := s.FeatureStats(ctx, core.StatsFilter{Environment: "local"})
	if g := byFeature(local); g["f1"].Runs != 1 || g["f1"].Plays != 2 {
		t.Errorf("local only: f1 = %+v; want 1 run, 2 plays", g["f1"])
	}
	only, _ := s.FeatureStats(ctx, core.StatsFilter{Feature: "f2"})
	if len(only) != 1 || only[0].Feature != "f2" {
		t.Errorf("feature filter: %+v; want only f2", only)
	}
	since, _ := s.FeatureStats(ctx, core.StatsFilter{Since: t0.Add(time.Hour)})
	if g := byFeature(since); len(g) != 1 || g["f1"].Runs != 1 || g["f1"].Plays != 1 {
		t.Errorf("since: %+v; want only the second run of f1", since)
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
		"FeatureStats": func() error {
			_, err := s.FeatureStats(cancelled, core.StatsFilter{})
			return err
		},
		"GetSnapshotFile": func() error {
			_, err := s.GetSnapshotFile(cancelled, id, "tests/a.sql")
			return err
		},
		"LatestTestFile": func() error {
			_, _, err := s.LatestTestFile(cancelled, "a")
			return err
		},
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
