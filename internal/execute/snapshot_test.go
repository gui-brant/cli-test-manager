package execute

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// fakeTree serves files from memory. ok says whether it can list them.
type fakeTree struct {
	data  map[string][]byte
	order []string
	ok    bool
	// readErr, when set for a path, is what reading it returns.
	readErr map[string]error
}

func newTree(ok bool, files map[string]string) *fakeTree {
	t := &fakeTree{data: map[string][]byte{}, ok: ok, readErr: map[string]error{}}
	for path, text := range files {
		t.set(path, text)
	}
	return t
}

func (t *fakeTree) set(path, text string) {
	if _, have := t.data[path]; !have {
		t.order = append(t.order, path)
	}
	t.data[path] = []byte(text)
}

func (t *fakeTree) Files(context.Context, string) ([]string, bool) {
	if !t.ok {
		return nil, false
	}
	return append([]string(nil), t.order...), true
}

func (t *fakeTree) Read(_ string, rel string) ([]byte, error) {
	if err := t.readErr[rel]; err != nil {
		return nil, err
	}
	data, ok := t.data[rel]
	if !ok {
		return nil, errors.New("no such file or directory")
	}
	return data, nil
}

// snapshotFixture is the usual fixture with path patterns on both features.
func snapshotFixture(t *testing.T, tr core.Tree) *fixture {
	t.Helper()
	f := newFixture(t)
	f.manifest.Features["accounts"] = core.FeatureSpec{Paths: []string{"db/**/*.sql", "tests/sql/**"}}
	f.manifest.Features["notes"] = core.FeatureSpec{Paths: []string{"docs/**"}}
	f.svc.Tree = tr
	return f
}

func (f *fixture) runSnapshot(env string, sel Selection) (Report, error) {
	return f.svc.Run(bg, Request{
		Manifest: f.manifest, Root: "/project", Environment: env, Select: sel,
		ManifestRaw: []byte("manifest: text\n"), ManifestFile: "my-manifest.yaml",
	})
}

func fileNames(files []core.SnapshotFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestTheSnapshotHoldsTheManifestTheTestsAndTheFeaturesFiles(t *testing.T) {
	tr := newTree(true, map[string]string{
		"tests/sql/users_table.sql":   "select 1;",
		"tests/http/login_flow.http":  "GET /login",
		"db/migrations/001_a.sql":     "create table a ();",
		"db/migrations/002_b.txt":     "not matched",
		"docs/readme.md":              "notes feature, not run",
		"tests/http/note_create.http": "POST /notes",
	})
	f := snapshotFixture(t, tr)
	rep, err := f.runSnapshot("local", Selection{Feature: "accounts"})
	if err != nil {
		t.Fatal(err)
	}
	stored := storedRun(t, f, rep)

	// Sorted by path; the manifest under the name the request gave; a file that is
	// both a test's file and matches the feature's patterns is one entry, as a test.
	want := []string{"db/migrations/001_a.sql", "my-manifest.yaml", "tests/http/login_flow.http", "tests/sql/users_table.sql"}
	if got := fileNames(stored.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v; want %v", got, want)
	}
	byPath := map[string]core.SnapshotFile{}
	for _, f := range stored.Files {
		byPath[f.Path] = f
	}
	if f := byPath["my-manifest.yaml"]; f.Role != core.RoleManifest || f.TestID != "" {
		t.Errorf("manifest = %+v", f)
	}
	if f := byPath["tests/sql/users_table.sql"]; f.Role != core.RoleTest || f.TestID != "users-table" {
		t.Errorf("a test file that also matches a pattern = %+v; want role test, once", f)
	}
	if f := byPath["db/migrations/001_a.sql"]; f.Role != core.RoleFeature || f.TestID != "" || f.Size != 18 || len(f.Hash) != 64 {
		t.Errorf("migration = %+v", f)
	}
	// The text is in the history, read back by path.
	got, err := f.store.GetSnapshotFile(bg, rep.RunID, "db/migrations/001_a.sql")
	if err != nil || string(got.Content) != "create table a ();" {
		t.Errorf("GetSnapshotFile = %q, %v", got.Content, err)
	}
}

func TestTheSnapshotCoversOnlyTheFeaturesThatRan(t *testing.T) {
	tr := newTree(true, map[string]string{
		"tests/http/note_create.http": "POST /notes", "docs/readme.md": "notes", "db/migrations/001_a.sql": "accounts",
	})
	f := snapshotFixture(t, tr)
	rep, _ := f.runSnapshot("local", Selection{Feature: "notes"})
	want := []string{"docs/readme.md", "my-manifest.yaml", "tests/http/note_create.http"}
	if got := fileNames(storedRun(t, f, rep).Files); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v; want %v (nothing of the accounts feature)", got, want)
	}
}

func TestTheDefaultManifestNameAndNoManifestAtAll(t *testing.T) {
	f := snapshotFixture(t, newTree(true, map[string]string{"tests/http/note_create.http": "x"}))
	rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local",
		Select: Selection{Feature: "notes"}, ManifestRaw: []byte("m")})
	if err != nil {
		t.Fatal(err)
	}
	if got := fileNames(storedRun(t, f, rep).Files); !reflect.DeepEqual(got, []string{"test-manifest.yaml", "tests/http/note_create.http"}) {
		t.Errorf("files = %v; want the default manifest name", got)
	}
	rep, err = f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Feature: "notes"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fileNames(storedRun(t, f, rep).Files); !reflect.DeepEqual(got, []string{"tests/http/note_create.http"}) {
		t.Errorf("files = %v; with no manifest text the manifest is not recorded", got)
	}
}

func TestWithNoTreeOnlyTheManifestIsRecorded(t *testing.T) {
	f := newFixture(t) // no Tree
	rep, err := f.runSnapshot("local", Selection{Feature: "accounts"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fileNames(storedRun(t, f, rep).Files); !reflect.DeepEqual(got, []string{"my-manifest.yaml"}) {
		t.Errorf("files = %v", got)
	}
	if len(rep.Notes) != 0 {
		t.Errorf("notes %v; not having a tree is not worth a note", rep.Notes)
	}
}

func TestWhenFilesCannotBeListedTheRunSaysSo(t *testing.T) {
	f := snapshotFixture(t, newTree(false, map[string]string{"tests/http/note_create.http": "POST /notes"}))
	rep, err := f.runSnapshot("local", Selection{Feature: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "could not be listed") {
		t.Errorf("notes = %v", rep.Notes)
	}
	// The tests' files and the manifest are still recorded; the run is unharmed.
	if got := fileNames(storedRun(t, f, rep).Files); !reflect.DeepEqual(got, []string{"my-manifest.yaml", "tests/http/note_create.http"}) {
		t.Errorf("files = %v", got)
	}
	if rep.Run.Verdict().Outcome != core.OutcomePass {
		t.Errorf("verdict %+v; a snapshot problem must not fail the run", rep.Run.Verdict())
	}

	// A feature with no patterns needs no listing, so there is nothing to say.
	f = newFixture(t)
	f.svc.Tree = newTree(false, nil)
	rep, _ = f.runSnapshot("local", Selection{Feature: "notes"})
	if len(rep.Notes) != 0 {
		t.Errorf("notes %v for features with no path patterns", rep.Notes)
	}
}

func TestWhatCannotBeKeptIsListedWithItsReasonAndNeverFailsTheRun(t *testing.T) {
	tr := newTree(true, map[string]string{
		"tests/http/note_create.http": "POST /notes",
		"docs/token.txt":              "key=pw-12345-abc",
		"docs/binary.dat":             "ab\x00cd",
		"docs/huge.txt":               strings.Repeat("x", MaxSnapshotFileBytes+1),
		"docs/exactly.txt":            strings.Repeat("y", MaxSnapshotFileBytes),
		"docs/unreadable.txt":         "never read",
	})
	tr.readErr["docs/unreadable.txt"] = errors.New("permission denied for pw-12345-abc")
	f := snapshotFixture(t, tr)
	rep, err := f.runSnapshot("deployed", Selection{Feature: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	notes := map[string]string{}
	for _, file := range storedRun(t, f, rep).Files {
		notes[file.Path] = file.Note
	}
	want := map[string]string{
		"docs/token.txt":      "contains a secret value",
		"docs/binary.dat":     "binary",
		"docs/huge.txt":       "larger than 1 MiB",
		"docs/exactly.txt":    "", // exactly the limit is kept
		"docs/unreadable.txt": "could not be read: permission denied for [redacted]",
		"my-manifest.yaml":    "", "tests/http/note_create.http": "",
	}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes:\n got  %v\n want %v", notes, want)
	}
	// A file that was listed but not kept still has its hash and size, except one
	// that could not be read at all.
	for _, file := range storedRun(t, f, rep).Files {
		switch file.Path {
		case "docs/token.txt", "docs/binary.dat", "docs/huge.txt":
			if len(file.Hash) != 64 || file.Size == 0 {
				t.Errorf("%s has hash %q, size %d; want both recorded", file.Path, file.Hash, file.Size)
			}
		case "docs/unreadable.txt":
			if file.Hash != "" || file.Size != 0 {
				t.Errorf("an unreadable file has hash %q, size %d", file.Hash, file.Size)
			}
		}
	}
	if rep.Run.Verdict().Outcome != core.OutcomePass {
		t.Errorf("verdict %+v", rep.Run.Verdict())
	}
}

// A run keeps at most 32 MiB of text. Past that, files are listed with a note.
func TestARunKeepsAtMostThirtyTwoMebibytesOfText(t *testing.T) {
	tr := newTree(true, map[string]string{"tests/http/note_create.http": "POST /notes"})
	full := strings.Repeat("z", MaxSnapshotFileBytes)
	for i := 0; i < 34; i++ {
		tr.set(fmt.Sprintf("docs/f%02d.txt", i), full)
	}
	f := snapshotFixture(t, tr)
	rep, err := f.runSnapshot("local", Selection{Feature: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	kept, limited := 0, 0
	for _, file := range storedRun(t, f, rep).Files {
		switch {
		case file.Note == "":
			kept++
		case strings.Contains(file.Note, "snapshot limit"):
			limited++
		}
	}
	// The manifest and the test's file are tiny; 32 whole mebibytes fit beside
	// them only up to the limit, so the last of the big files are listed only.
	if limited < 2 || kept < 31 {
		t.Errorf("%d files kept, %d over the limit; want about 32 kept and the rest limited", kept, limited)
	}
}

// The files are read before the first test, so a test that edits one cannot
// change what the run says it was made with.
func TestTheSnapshotIsTakenBeforeTheFirstTest(t *testing.T) {
	tr := newTree(true, map[string]string{"tests/http/note_create.http": "POST /notes\n"})
	f := snapshotFixture(t, tr)
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		tr.set("tests/http/note_create.http", "EDITED BY THE TEST\n") // a test that rewrites its own file
		return defaultReply(n, inv)
	}
	rep, err := f.runSnapshot("local", Selection{Feature: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.GetSnapshotFile(bg, rep.RunID, "tests/http/note_create.http")
	if err != nil || string(got.Content) != "POST /notes\n" {
		t.Errorf("recorded %q, %v; want the text from before the test ran", got.Content, err)
	}
}

// A run that is cancelled saves nothing, snapshot included.
func TestACancelledRunSavesNoSnapshot(t *testing.T) {
	f := snapshotFixture(t, newTree(true, map[string]string{"tests/http/note_create.http": "x"}))
	ctx, cancel := context.WithCancel(bg)
	cancel()
	rep, err := f.svc.Run(ctx, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Feature: "notes"}, ManifestRaw: []byte("m")})
	if err == nil || rep.Saved {
		t.Fatalf("a cancelled run: saved %v, error %v", rep.Saved, err)
	}
	if n := f.storedRuns(); n != 0 {
		t.Errorf("%d runs stored", n)
	}
}
