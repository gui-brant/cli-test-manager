package sqlite

import (
	"bytes"
	"context"
	"database/sql"
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
	for round := 0; round < 5; round++ {
		path := newPath(t)
		errs := make(chan error, 4)
		stores := make(chan *Store, 4)
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
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
