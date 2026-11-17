package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/store/storetest"
)

var bg = context.Background()

func newPath(t *testing.T) string { return filepath.Join(t.TempDir(), "history.sqlite") }

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func someRun(n int) core.Run {
	return core.Run{
		Source: core.SourceLocal, Environment: "local", State: core.RunComplete,
		StartedAt: time.Date(2026, 11, 10, 12, 0, n, 0, time.UTC),
		Results: []core.Result{
			{TestID: "a", Feature: "f", Name: "A", Status: core.StatusPass},
			{TestID: "b", Feature: "f", Name: "B", Status: core.StatusFail},
		},
	}
}

// rawDB opens the file without going through Open, to look inside it.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func pragma(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) core.Store { return openAt(t, newPath(t)) })
}

func TestOpenCreatesTheFolderAndAPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".tester", "deep", "history.sqlite")
	openAt(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the file has permissions %o; want 600 (owner only)", perm)
	}
}

func TestOpenKeepsAnExistingFilesPermissions(t *testing.T) {
	path := newPath(t)
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	openAt(t, path)
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("an existing file's permissions became %o; want them left at 640", perm)
	}
}

func TestPathsWithSpacesAndSpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my history & (test) #1 ? 100%")
	path := filepath.Join(dir, "history.sqlite")
	s := openAt(t, path)
	if _, err := s.SaveRun(bg, someRun(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the history file is not at the path given: %v", err)
	}
}

func TestReopeningKeepsTheData(t *testing.T) {
	path := newPath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.SaveRun(bg, someRun(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again := openAt(t, path)
	got, err := again.GetRun(bg, id)
	if err != nil || len(got.Results) != 2 {
		t.Errorf("after reopening: %+v, %v; want the saved run with 2 results", got, err)
	}
}

func TestHeaderAndConnectionSettings(t *testing.T) {
	s := openAt(t, newPath(t))
	if got := pragma(t, s.db, "application_id"); got != applicationID {
		t.Errorf("application_id = %#x; want %#x", got, applicationID)
	}
	if got := pragma(t, s.db, "user_version"); got != len(migrations) {
		t.Errorf("user_version = %d; want %d", got, len(migrations))
	}
	if got := pragma(t, s.db, "foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d; want 1", got)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
}

func TestRefusesAFileFromANewerVersion(t *testing.T) {
	path := newPath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	before, _ := os.ReadFile(path)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("Open of a newer file: %v; want an error about a newer version", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("refusing a newer file still changed it")
	}
}

func TestRefusesAnotherSQLiteDatabase(t *testing.T) {
	path := newPath(t)
	db := rawDB(t, path)
	if _, err := db.Exec("CREATE TABLE customers (name TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	before, _ := os.ReadFile(path)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "not a history file") {
		t.Fatalf("Open of another database: %v; want an error saying it is not a history file", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("refusing another database still changed it")
	}
}

func TestRefusesAFileThatIsNotSQLite(t *testing.T) {
	path := newPath(t)
	if err := os.WriteFile(path, []byte("this is just some text, long enough to be mistaken for nothing at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("Open of a text file returned no error")
	}
}

func TestAFailedUpgradeRollsBackCompletely(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })
	migrations = [][]string{{"CREATE TABLE a (x INTEGER)", "THIS IS NOT SQL"}}

	path := newPath(t)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "upgrading") {
		t.Fatalf("Open with a broken upgrade: %v; want an upgrade error", err)
	}

	db := rawDB(t, path)
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil || tables != 0 {
		t.Errorf("%d tables left behind (%v); the failed upgrade must leave nothing", tables, err)
	}
	if v, id := pragma(t, db, "user_version"), pragma(t, db, "application_id"); v != 0 || id != 0 {
		t.Errorf("header changed to version %d, id %#x by a failed upgrade", v, id)
	}
}

func TestUpgradeAppliesOnlyTheMissingSteps(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = [][]string{{"CREATE TABLE t1 (x INTEGER)"}}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO t1 VALUES (7)"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// A newer program with a second step opens the same file.
	migrations = [][]string{
		{"CREATE TABLE t1 (x INTEGER)"},
		{"CREATE TABLE t2 (y INTEGER)", "INSERT INTO t2 VALUES (1)"},
	}
	s = openAt(t, path)
	if got := pragma(t, s.db, "user_version"); got != 2 {
		t.Errorf("version after upgrade = %d; want 2", got)
	}
	var x, y int
	if err := s.db.QueryRow("SELECT x FROM t1").Scan(&x); err != nil || x != 7 {
		t.Errorf("t1 data = %d, %v; the earlier data must survive", x, err)
	}
	if err := s.db.QueryRow("SELECT y FROM t2").Scan(&y); err != nil || y != 1 {
		t.Errorf("t2 data = %d, %v; the new step must have run once", y, err)
	}
}

// A history file written by the first schema (before runs had a detail) opens
// with the real migrations: its run is still there, its detail is empty, and
// new runs can save a detail.
func TestAVersion1FileGainsTheDetailColumn(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = old[:1] // the schema as it was
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := formatTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	if _, err := s.db.Exec(`INSERT INTO run (source, environment, state, started_at, finished_at, exit_code,
		external_id, attempt, commit_sha, diagnostic, outcome, reason, total, failed)
		VALUES ('local', 'local', 'complete', ?, ?, 0, '', 0, '', 'an old diagnostic', 'fail', 'x', 0, 0)`, at, at); err != nil {
		t.Fatal(err)
	}
	s.Close()

	migrations = old // the program as it is now
	s = openAt(t, path)
	if got := pragma(t, s.db, "user_version"); got != len(old) {
		t.Errorf("version after upgrade = %d; want %d", got, len(old))
	}
	run, err := s.GetRun(bg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.Diagnostic != "an old diagnostic" || run.Detail != "" {
		t.Errorf("old run: diagnostic %q, detail %q; want its diagnostic kept and an empty detail", run.Diagnostic, run.Detail)
	}
	fresh := someRun(1)
	fresh.Detail = "[a] A\n  at a.js:1"
	id, err := s.SaveRun(bg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRun(bg, id); err != nil || got.Detail != fresh.Detail {
		t.Errorf("new run detail %q, %v; want %q", got.Detail, err, fresh.Detail)
	}
}

// Version 3 rebuilds the run table. That must keep every run and, above all, its
// results: dropping the old table with foreign keys on would delete them.
func TestAVersion2FileSurvivesTheRebuild(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = old[:2] // the schema as it was
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := formatTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	for _, stmt := range []string{
		`INSERT INTO run (id, source, environment, state, started_at, finished_at, exit_code, external_id, attempt,
			commit_sha, diagnostic, detail, outcome, reason, total, failed)
			VALUES (7, 'local', 'local', 'complete', '` + at + `', '` + at + `', 0, '', 0, '', '', 'old detail', 'fail', 'x', 2, 1)`,
		`INSERT INTO run (id, source, environment, state, started_at, finished_at, exit_code, external_id, attempt,
			commit_sha, diagnostic, detail, outcome, reason, total, failed)
			VALUES (8, 'remote', 'deployed', 'complete', '` + at + `', '` + at + `', 0, '555', 1, 'abc', '', '', 'pass', '', 1, 0)`,
		`INSERT INTO result (run_id, position, test_id, feature, name, status, duration_ns, message)
			VALUES (7, 0, 'a', 'f', 'A', 'pass', 5, ''), (7, 1, 'b', 'f', 'B', 'fail', 6, 'broke'), (8, 0, 'a', 'f', 'A', 'pass', 7, '')`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	migrations = old // the program as it is now
	s = openAt(t, path)
	if got := pragma(t, s.db, "user_version"); got != len(old) {
		t.Fatalf("version after upgrade = %d; want %d", got, len(old))
	}

	run, err := s.GetRun(bg, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Results) != 2 || run.Results[1].Message != "broke" || run.Detail != "old detail" {
		t.Errorf("run 7 after the rebuild: %d results, detail %q; want both results and its detail", len(run.Results), run.Detail)
	}
	if run.Dirty || run.Trigger != "" || len(run.Tools) != 0 {
		t.Errorf("an old run gained metadata: dirty %v, trigger %q, tools %v", run.Dirty, run.Trigger, run.Tools)
	}
	if got, err := s.FindByKey(bg, "555", 1); err != nil || got.ID != 8 || len(got.Results) != 1 {
		t.Errorf("FindByKey after the rebuild: %+v, %v; the keyed run and its result must survive", got, err)
	}

	// Foreign keys are back on, so a result for a missing run is refused...
	if got := pragma(t, s.db, "foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d after the upgrade; want 1", got)
	}
	if _, err := s.db.Exec(`INSERT INTO result (run_id, position, test_id, feature, name, status, duration_ns, message)
		VALUES (999, 0, '', '', '', 'pass', 0, '')`); err == nil {
		t.Error("a result for a missing run was accepted")
	}
	// ...the unique key is back, so the same remote run cannot be stored twice...
	if _, err := s.db.Exec(`INSERT INTO run (source, environment, state, started_at, finished_at, exit_code, external_id, attempt,
		commit_sha, diagnostic, outcome, reason, total, failed)
		VALUES ('remote', 'deployed', 'complete', '', '', 0, '555', 1, '', '', 'pass', '', 0, 0)`); err == nil {
		t.Error("a second run with the same key was accepted")
	}
	// ...and the new source is allowed.
	imported := someRun(3)
	imported.Source, imported.Environment = core.SourceImport, "network"
	if id, err := s.SaveRun(bg, imported); err != nil {
		t.Errorf("saving an imported run: %v", err)
	} else if got, err := s.GetRun(bg, id); err != nil || got.Source != core.SourceImport {
		t.Errorf("imported run came back as %+v, %v", got.Source, err)
	}
}

// A rebuild that fails halfway changes nothing: the old table and its results
// are still there, at the old version.
func TestAFailedRebuildChangesNothing(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = old[:2]
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := formatTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	for _, stmt := range []string{
		`INSERT INTO run (id, source, environment, state, started_at, finished_at, exit_code, external_id, attempt,
			commit_sha, diagnostic, detail, outcome, reason, total, failed)
			VALUES (1, 'local', 'local', 'complete', '` + at + `', '` + at + `', 0, '', 0, '', '', '', 'fail', 'x', 2, 1)`,
		`INSERT INTO result (run_id, position, test_id, feature, name, status, duration_ns, message)
			VALUES (1, 0, 'a', 'f', 'A', 'pass', 5, ''), (1, 1, 'b', 'f', 'B', 'fail', 6, 'broke')`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	// A version 3 that starts the rebuild, drops the old table, then breaks.
	migrations = [][]string{old[0], old[1], {
		`CREATE TABLE run_new (id INTEGER PRIMARY KEY)`,
		`DROP TABLE run`,
		`THIS IS NOT SQL`,
	}}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "upgrading") {
		t.Fatalf("Open with a broken rebuild: %v; want an upgrade error", err)
	}

	db := rawDB(t, path)
	var runs, results int
	if err := db.QueryRow("SELECT count(*) FROM run").Scan(&runs); err != nil || runs != 1 {
		t.Errorf("%d runs after the failed rebuild (%v); want the original 1", runs, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM result").Scan(&results); err != nil || results != 2 {
		t.Errorf("%d results after the failed rebuild (%v); want the original 2", results, err)
	}
	if got := pragma(t, db, "user_version"); got != 2 {
		t.Errorf("version = %d after the failed rebuild; want 2", got)
	}
}

// An upgrade switches foreign keys off for its own connection. Whatever
// happens, that connection must go back to the pool with them on again.
func TestForeignKeysAreBackOnAfterAnUpgradeStep(t *testing.T) {
	s := openAt(t, newPath(t))
	inside := -1
	err := s.writeTxFK(bg, false, func(conn *sql.Conn) error {
		if err := conn.QueryRowContext(bg, "PRAGMA foreign_keys").Scan(&inside); err != nil {
			return err
		}
		return sql.ErrConnDone // any failure: the transaction rolls back
	})
	if err == nil {
		t.Fatal("the failing step reported success")
	}
	if inside != 0 {
		t.Errorf("foreign_keys = %d inside the step; want 0", inside)
	}
	if got := pragma(t, s.db, "foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d afterwards; want 1", got)
	}
}

// The same text in many runs is one row of text, however many runs list it.
func TestIdenticalTextIsStoredOnce(t *testing.T) {
	s := openAt(t, newPath(t))
	text := []byte("create table t ();")
	sum := sha256.Sum256(text)
	hash := hex.EncodeToString(sum[:])
	for i := 0; i < 3; i++ {
		run := someRun(i)
		run.Files = []core.SnapshotFile{{Path: "db/m.sql", Role: core.RoleFeature, Hash: hash, Size: int64(len(text)), Content: text}}
		if _, err := s.SaveRun(bg, run); err != nil {
			t.Fatal(err)
		}
	}
	var blobs, listed int
	if err := s.db.QueryRow("SELECT count(*) FROM blob").Scan(&blobs); err != nil || blobs != 1 {
		t.Errorf("%d texts stored (%v); want 1", blobs, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM run_file").Scan(&listed); err != nil || listed != 3 {
		t.Errorf("%d listed files (%v); want 3, one for each run", listed, err)
	}

	// A file whose text was not kept stores no text at all.
	run := someRun(9)
	run.Files = []core.SnapshotFile{{Path: "secret.env", Role: core.RoleFeature, Hash: "ff", Size: 5, Note: "contains a secret value"}}
	if _, err := s.SaveRun(bg, run); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM blob").Scan(&blobs); err != nil || blobs != 1 {
		t.Errorf("%d texts stored after a file that was not kept; want still 1", blobs)
	}
}

// Deleting a run removes its list of files; the texts stay for the runs that
// share them (nothing deletes a text yet).
func TestDeletingARunDeletesItsFileList(t *testing.T) {
	s := openAt(t, newPath(t))
	run := someRun(1)
	run.Files = []core.SnapshotFile{{Path: "a", Role: core.RoleManifest, Hash: "h", Size: 1, Content: []byte("x")}}
	id, err := s.SaveRun(bg, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM run WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	var listed int
	if err := s.db.QueryRow("SELECT count(*) FROM run_file").Scan(&listed); err != nil || listed != 0 {
		t.Errorf("%d listed files left after deleting their run (%v); want 0", listed, err)
	}
}

// A file from the version before snapshots opens, keeps its runs, and can then
// save a run with files.
func TestAVersion3FileGainsTheSnapshotTables(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = old[:3] // the schema as it was
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
	run, err := s.GetRun(bg, 1)
	if err != nil || len(run.Results) != 2 || len(run.Files) != 0 {
		t.Errorf("the old run: %d results, %d files, %v; want its results and no files", len(run.Results), len(run.Files), err)
	}
	fresh := someRun(2)
	fresh.Files = []core.SnapshotFile{{Path: "a.sql", Role: core.RoleTest, TestID: "a", Hash: "h", Size: 3, Content: []byte("one")}}
	id, err := s.SaveRun(bg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := s.GetSnapshotFile(bg, id, "a.sql"); err != nil || string(f.Content) != "one" {
		t.Errorf("a file saved after the upgrade: %+v, %v", f, err)
	}
}

// A file from the version before refused runs opens, keeps its runs with their
// results and files, and can then save a run that was refused. Before the
// upgrade the old file cannot hold one: its state list does not have 'refused'.
func TestAVersion4FileGainsTheRefusedState(t *testing.T) {
	old := migrations
	t.Cleanup(func() { migrations = old })

	path := newPath(t)
	migrations = old[:4] // the schema as it was
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	withFile := someRun(1)
	withFile.Files = []core.SnapshotFile{{Path: "a.sql", Role: core.RoleTest, TestID: "a", Hash: "h", Size: 3, Content: []byte("one")}}
	withFile.Tools = []core.Tool{{Name: "pgtap", Version: "1.3"}}
	if _, err := s.SaveRun(bg, withFile); err != nil {
		t.Fatal(err)
	}
	refused := someRun(2)
	refused.State, refused.Results = core.RunRefused, nil
	if _, err := s.SaveRun(bg, refused); err == nil {
		t.Fatal("a version 4 file saved a refused run; its state list should not allow it")
	}
	s.Close()

	migrations = old // the program as it is now
	s = openAt(t, path)
	if got := pragma(t, s.db, "user_version"); got != len(old) {
		t.Fatalf("version after upgrade = %d; want %d", got, len(old))
	}
	kept, err := s.GetRun(bg, 1)
	if err != nil || len(kept.Results) != 2 || len(kept.Files) != 1 || len(kept.Tools) != 1 {
		t.Errorf("the old run: %d results, %d files, %d tools, %v; want 2, 1, 1", len(kept.Results), len(kept.Files), len(kept.Tools), err)
	}
	if f, err := s.GetSnapshotFile(bg, 1, "a.sql"); err != nil || string(f.Content) != "one" {
		t.Errorf("the old run's file after the rebuild: %+v, %v", f, err)
	}

	refused.Diagnostic = "precondition migrations-applied is not met: Run `supabase db reset`\n  the check said: 2 migrations are not applied"
	refused.Trigger, refused.CommitSHA, refused.Dirty = core.TriggerHook, "abc123", true
	id, err := s.SaveRun(bg, refused)
	if err != nil {
		t.Fatalf("a refused run after the upgrade: %v", err)
	}
	got, err := s.GetRun(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.RunRefused || len(got.Results) != 0 || got.Diagnostic != refused.Diagnostic ||
		got.Trigger != core.TriggerHook || got.CommitSHA != "abc123" || !got.Dirty {
		t.Errorf("the refused run read back as %+v", got)
	}
	if v := got.Verdict(); v.Outcome != core.OutcomeUndetermined {
		t.Errorf("a refused run's outcome = %q; want %q (it is neither a pass nor a fail)", v.Outcome, core.OutcomeUndetermined)
	}
}

func TestDeletingARunDeletesItsResults(t *testing.T) {
	s := openAt(t, newPath(t))
	id, err := s.SaveRun(bg, someRun(1))
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM result WHERE run_id = ?", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(); got != 2 {
		t.Fatalf("%d results before the delete; want 2", got)
	}
	if _, err := s.db.Exec("DELETE FROM run WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Errorf("%d results left after deleting their run; want 0", got)
	}
}

// A push hook, a pull and the menu can all write to the same file at once.
func TestConcurrentWritersAndAReader(t *testing.T) {
	path := newPath(t)
	a, b := openAt(t, path), openAt(t, path) // two connections, as two programs would have

	const writers, each = 8, 25
	errs := make(chan error, writers*each+100)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			s := a
			if w%2 == 1 {
				s = b
			}
			for i := 0; i < each; i++ {
				if _, err := s.SaveRun(bg, someRun(w*each+i)); err != nil {
					errs <- err
				}
			}
		}(w)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := b.ListRuns(bg, core.RunFilter{Limit: 10}); err != nil {
					errs <- err
				}
			}
		}
	}()

	wg.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent use failed: %v", err)
	}

	all, err := a.ListRuns(bg, core.RunFilter{Limit: 1000})
	if err != nil || len(all) != writers*each {
		t.Errorf("%d runs saved (%v); want %d", len(all), err, writers*each)
	}
}

// Two programs opening a brand-new file together must not both create the
// tables.
func TestConcurrentOpenOfANewFile(t *testing.T) {
	const rounds, openers = 25, 8
	for round := 0; round < rounds; round++ {
		path := newPath(t)
		errs := make(chan error, openers)
		stores := make(chan *Store, openers)
		var wg sync.WaitGroup
		for j := 0; j < openers; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := Open(path)
				if err != nil {
					errs <- err
					return
				}
				stores <- s
			}()
		}
		wg.Wait()
		close(errs)
		close(stores)
		for err := range errs {
			t.Errorf("round %d: Open failed: %v", round, err)
		}
		for s := range stores {
			s.Close()
		}
	}
}
