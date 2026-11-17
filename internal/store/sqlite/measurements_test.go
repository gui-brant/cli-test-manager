package sqlite

import (
	"reflect"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

func measuredRun(n int) core.Run {
	run := someRun(n)
	run.Results[0].Measurements = []core.Measurement{{Name: "p95_ms", Value: 187}, {Name: "rate_per_s", Value: 101.5}}
	run.Results[1].Measurements = []core.Measurement{{Name: "p95_ms", Value: 900}} // a failed result keeps its numbers too
	run.Results = append(run.Results, core.Result{TestID: "c", Feature: "f", Name: "C", Status: core.StatusPass})
	return run
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMeasurementsComeBackWithTheirResults(t *testing.T) {
	s := openAt(t, newPath(t))
	id, err := s.SaveRun(bg, measuredRun(1))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	want := measuredRun(1)
	for i := range want.Results {
		if !reflect.DeepEqual(got.Results[i].Measurements, want.Results[i].Measurements) {
			t.Errorf("result %d measurements = %v; want %v", i, got.Results[i].Measurements, want.Results[i].Measurements)
		}
	}
	if got.Results[2].Measurements != nil {
		t.Errorf("a result with none came back with %v", got.Results[2].Measurements)
	}
	if n := countRows(t, s, "measurement"); n != 3 {
		t.Errorf("%d rows in the measurement table; want 3", n)
	}
}

func TestReplacingARunReplacesItsMeasurements(t *testing.T) {
	s := openAt(t, newPath(t))
	remote := measuredRun(1)
	remote.Source, remote.ExternalID, remote.Attempt = core.SourceRemote, "ci-77", 1
	id, err := s.SaveRun(bg, remote)
	if err != nil {
		t.Fatal(err)
	}

	again := someRun(1)
	again.Source, again.ExternalID, again.Attempt = core.SourceRemote, "ci-77", 1
	again.Results[0].Measurements = []core.Measurement{{Name: "only_one", Value: 5}}
	if id2, err := s.SaveRun(bg, again); err != nil || id2 != id {
		t.Fatalf("saving the same key again: id %d, %v; want id %d", id2, err, id)
	}
	if n := countRows(t, s, "measurement"); n != 1 {
		t.Errorf("%d rows in the measurement table after the replacement; want 1 (the old numbers go with the old results)", n)
	}
	got, _ := s.GetRun(bg, id)
	if !reflect.DeepEqual(got.Results[0].Measurements, again.Results[0].Measurements) || got.Results[1].Measurements != nil {
		t.Errorf("after the replacement: %v and %v", got.Results[0].Measurements, got.Results[1].Measurements)
	}
}

func TestDeletingARunDeletesItsMeasurements(t *testing.T) {
	s := openAt(t, newPath(t))
	id, err := s.SaveRun(bg, measuredRun(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM run WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "measurement"); n != 0 {
		t.Errorf("%d measurements left after deleting their run; want 0", n)
	}
}

// A file from before measurements opens, keeps its runs, and can then save
// numbers.
func TestAVersion5FileGainsTheMeasurementTable(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = old[:5] // the schema as it was
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRun(bg, someRun(1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	migrations = old // the program as it is now
	s = openAt(t, path)
	if got := pragma(t, s.db, "user_version"); got != len(old) {
		t.Fatalf("version after upgrade = %d; want %d", got, len(old))
	}
	kept, err := s.GetRun(bg, 1)
	if err != nil || len(kept.Results) != 2 || kept.Results[0].Measurements != nil {
		t.Errorf("the old run: %d results, measurements %v, %v; want its results and no numbers", len(kept.Results), kept.Results[0].Measurements, err)
	}
	id, err := s.SaveRun(bg, measuredRun(2))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRun(bg, id); len(got.Results[0].Measurements) != 2 {
		t.Errorf("numbers saved after the upgrade: %v", got.Results[0].Measurements)
	}
}
