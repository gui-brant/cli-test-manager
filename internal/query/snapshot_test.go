package query

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

func snapService(t *testing.T) (*Service, *sqlite.Store) {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{Store: st}, st
}

func runWith(started time.Time, commit string, dirty bool, files ...core.SnapshotFile) core.Run {
	return core.Run{
		Source: core.SourceLocal, Environment: "local", State: core.RunComplete,
		StartedAt: started, FinishedAt: started, CommitSHA: commit, Dirty: dirty,
		Results: []core.Result{{TestID: "a", Feature: "f", Name: "A", Status: core.StatusPass}},
		Files:   files,
	}
}

func testFile(text string) core.SnapshotFile {
	return core.SnapshotFile{Path: "tests/a.sql", Role: core.RoleTest, TestID: "a", Hash: "h" + text, Size: int64(len(text)), Content: []byte(text)}
}

func TestSnapshotOfATestIsTheLatestRunsOrTheNamedOne(t *testing.T) {
	s, st := snapService(t)
	t0 := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	first, _ := st.SaveRun(context.Background(), runWith(t0, "c1", false, testFile("one")))
	second, _ := st.SaveRun(context.Background(), runWith(t0.Add(time.Hour), "c2", true, testFile("two")))

	latest, err := s.TestSnapshot(context.Background(), "a", 0)
	if err != nil || latest.RunID != second || string(latest.File.Content) != "two" || latest.Commit != "c2" || !latest.Dirty || !latest.StartedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("latest = %+v, %v", latest, err)
	}
	named, err := s.TestSnapshot(context.Background(), "a", first)
	if err != nil || named.RunID != first || string(named.File.Content) != "one" || named.Commit != "c1" || named.Dirty {
		t.Errorf("named = %+v, %v", named, err)
	}
}

func onlyErr(_ Snapshot, err error) error { return err }

// Every refusal matches core.ErrNotFound and reads as its own sentence.
func TestSnapshotErrorsMatchNotFoundAndReadCleanly(t *testing.T) {
	s, st := snapService(t)
	ctx := context.Background()
	id, _ := st.SaveRun(ctx, runWith(time.Now(), "", false, testFile("one")))
	noFiles, _ := st.SaveRun(ctx, runWith(time.Now(), "", false))

	cases := map[string]struct {
		err  error
		want string
	}{
		"never played":   {onlyErr(s.TestSnapshot(ctx, "b", 0)), `test "b" has not been run by the tester yet, so there is no snapshot of its file: run it once`},
		"no such run":    {onlyErr(s.TestSnapshot(ctx, "a", 99)), "run 99 not found"},
		"not in the run": {onlyErr(s.TestSnapshot(ctx, "b", id)), `run 1 did not record a file for test "b"`},
		"run with none":  {onlyErr(s.TestSnapshot(ctx, "a", noFiles)), `did not record a file for test "a"`},
		"file of no run": {onlyErr(s.RunFile(ctx, 99, "x")), "run 99 not found"},
		"no such file":   {onlyErr(s.RunFile(ctx, id, "nope.sql")), `run 1 has no file "nope.sql" (see tester history 1 --files)`},
	}
	for name, c := range cases {
		if !errors.Is(c.err, core.ErrNotFound) {
			t.Errorf("%s: %v does not match ErrNotFound", name, c.err)
			continue
		}
		if !strings.Contains(c.err.Error(), c.want) {
			t.Errorf("%s: %q; want it to contain %q", name, c.err, c.want)
		}
		if strings.HasSuffix(c.err.Error(), ": not found") {
			t.Errorf("%s: %q ends with the sentinel's text", name, c.err)
		}
	}
}

func TestRunFileReturnsAnyFileOfTheRun(t *testing.T) {
	s, st := snapService(t)
	manifest := core.SnapshotFile{Path: "test-manifest.yaml", Role: core.RoleManifest, Hash: "m", Size: 3, Content: []byte("yml")}
	id, _ := st.SaveRun(context.Background(), runWith(time.Now(), "c", false, manifest, testFile("one")))
	got, err := s.RunFile(context.Background(), id, "test-manifest.yaml")
	if err != nil || got.File.Role != core.RoleManifest || string(got.File.Content) != "yml" || got.RunID != id || got.Commit != "c" {
		t.Errorf("RunFile = %+v, %v", got, err)
	}
}
