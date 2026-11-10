package query

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

var (
	bg = context.Background()
	t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
)

func testManifest() *core.Manifest {
	return &core.Manifest{
		Features: []string{"accounts", "notes"},
		Tests: []core.TestSpec{
			{ID: "users-table", Name: "Users table", Feature: "accounts"},
			{ID: "login-flow", Name: "Login flow", Feature: "accounts"},
			{ID: "note-create", Name: "Create a note", Feature: "notes"},
		},
	}
}

func newService(t *testing.T) (*Service, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Service{Store: store}, store
}

func result(testID, feature, name string, st core.Status) core.Result {
	return core.Result{TestID: testID, Feature: feature, Name: name, Status: st}
}

func local(started time.Time, results ...core.Result) core.Run {
	return core.Run{
		Source: core.SourceLocal, Environment: "local", State: core.RunComplete,
		StartedAt: started, FinishedAt: started.Add(time.Second), Results: results,
	}
}

func save(t *testing.T, s core.Store, r core.Run) int64 {
	t.Helper()
	id, err := s.SaveRun(bg, r)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTestsAreNumberedByTheirPlaceInTheWholeList(t *testing.T) {
	m := testManifest()

	all, err := Tests(m, "")
	if err != nil {
		t.Fatal(err)
	}
	var numbers []int
	for _, r := range all {
		numbers = append(numbers, r.Number)
	}
	if !reflect.DeepEqual(numbers, []int{1, 2, 3}) {
		t.Errorf("numbers %v; want 1, 2, 3", numbers)
	}

	// A feature filter keeps each test's own number: test 3 is still test 3.
	notes, err := Tests(m, "notes")
	if err != nil || len(notes) != 1 || notes[0].Number != 3 || notes[0].ID != "note-create" {
		t.Errorf("notes: %+v, %v; want note-create numbered 3", notes, err)
	}
	if _, err := Tests(m, "billing"); err == nil {
		t.Error("an unknown feature was accepted")
	}
}

func TestFindTestAndNumbers(t *testing.T) {
	m := testManifest()
	if row, ok := FindTest(m, "login-flow"); !ok || row.Number != 2 {
		t.Errorf("FindTest(login-flow) = %+v, %v; want number 2", row, ok)
	}
	if _, ok := FindTest(m, "nope"); ok {
		t.Error("FindTest found a test that is not there")
	}
	if n := Numbers(nil); len(n) != 0 {
		t.Errorf("Numbers(nil) = %v; want empty", n)
	}
}

func TestRunDetailNumbersTheResults(t *testing.T) {
	svc, store := newService(t)
	id := save(t, store, local(t0,
		result("users-table", "accounts", "first", core.StatusPass),
		result("login-flow", "accounts", "second", core.StatusFail),
		result("retired-test", "accounts", "gone", core.StatusPass), // no longer in the manifest
		core.Result{Name: "unlisted", Status: core.StatusPass},      // belongs to no test
	))

	d, err := svc.Run(bg, testManifest(), id)
	if err != nil {
		t.Fatal(err)
	}
	var numbers []int
	for _, r := range d.Results {
		numbers = append(numbers, r.Number)
	}
	if !reflect.DeepEqual(numbers, []int{1, 2, 0, 0}) {
		t.Errorf("numbers %v; want 1, 2, then 0 for a retired test and for an unlisted result", numbers)
	}
	if d.Verdict != d.Run.Verdict() || d.Verdict.Outcome != core.OutcomeFail {
		t.Errorf("verdict %+v; want the core's verdict for the run", d.Verdict)
	}
	if d.Results[1].Name != "second" || d.Results[1].Status != core.StatusFail {
		t.Errorf("result fields were lost: %+v", d.Results[1])
	}

	if _, err := svc.Run(bg, testManifest(), 424242); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a missing run: %v; want core.ErrNotFound", err)
	}
}

func TestCountsComeInManifestOrderThenRetiredTests(t *testing.T) {
	svc, store := newService(t)
	save(t, store, local(t0,
		result("retired-test", "accounts", "Old", core.StatusPass),
		result("note-create", "notes", "Create", core.StatusPass),
		result("users-table", "accounts", "Users", core.StatusFail),
		core.Result{Name: "unlisted", Status: core.StatusFail}, // never counted
	))

	rows, err := svc.Counts(bg, testManifest(), core.StatsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.TestID)
	}
	if want := []string{"users-table", "note-create", "retired-test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v; want %v", got, want)
	}
	if rows[0].Number != 1 || rows[1].Number != 3 || rows[2].Number != 0 {
		t.Errorf("numbers %d, %d, %d; want 1, 3, 0", rows[0].Number, rows[1].Number, rows[2].Number)
	}
	if rows[0].Failed != 1 || rows[0].Feature != "accounts" {
		t.Errorf("counts or latest fields lost: %+v", rows[0])
	}
	// A listed test shows the manifest's name, not the name of its latest result.
	if rows[0].Name != "Users table" || rows[1].Name != "Create a note" {
		t.Errorf("names %q, %q; want the manifest's names", rows[0].Name, rows[1].Name)
	}
	// A retired test keeps the name last recorded.
	if rows[2].Name != "Old" {
		t.Errorf("retired test name %q; want the recorded name %q", rows[2].Name, "Old")
	}
}

func TestHistoryPassesTheFilterThrough(t *testing.T) {
	svc, store := newService(t)
	save(t, store, local(t0, result("a", "f", "A", core.StatusPass)))
	remote := local(t0.Add(time.Minute), result("a", "f", "A", core.StatusPass))
	remote.Source, remote.ExternalID, remote.Attempt = core.SourceRemote, "9001", 1
	save(t, store, remote)

	rows, err := svc.History(bg, core.RunFilter{Source: core.SourceRemote})
	if err != nil || len(rows) != 1 || rows[0].ExternalID != "9001" {
		t.Errorf("History = %+v, %v; want only the remote run", rows, err)
	}
}
