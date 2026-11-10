package manifestfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const goodManifest = `
manifest_version: 1
project: p
history: .tester/history.sqlite
features: [a]
environments:
  local: { vars: {}, secrets: [] }
runners:
  sql: { run: ["tool", "{files}"], format: tap, output: stdout, environments: [local], batch: true }
tests:
  - { id: one, name: One, feature: a, runner: sql, file: tests/a.sql }
  - { id: two, name: Two, feature: a, runner: sql, file: tests/b.sql }
`

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindReturnsTheNearestManifest(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, FileName), goodManifest)
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Find(nested)
	if err != nil || got != filepath.Join(root, FileName) {
		t.Fatalf("Find = %q, %v; want the manifest at the root", got, err)
	}

	inner := filepath.Join(root, "a", FileName)
	write(t, inner, goodManifest)
	if got, err := Find(nested); err != nil || got != inner {
		t.Errorf("Find = %q, %v; want the nearer manifest %q", got, err, inner)
	}
}

func TestFindSkipsAFolderNamedLikeTheManifest(t *testing.T) {
	parent := t.TempDir()
	write(t, filepath.Join(parent, FileName), goodManifest)
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(filepath.Join(child, FileName), 0o755); err != nil { // a folder, not a file
		t.Fatal(err)
	}
	got, err := Find(child)
	if err != nil || got != filepath.Join(parent, FileName) {
		t.Errorf("Find = %q, %v; want the real manifest in the parent", got, err)
	}
}

func TestFindWhenThereIsNone(t *testing.T) {
	_, err := Find(t.TempDir())
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("error = %v; want one wrapping core.ErrNotFound", err)
	}
	if err != nil && !strings.Contains(err.Error(), FileName) {
		t.Errorf("error %q does not name the file it looked for", err)
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	write(t, path, goodManifest)

	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != path || l.Root != root {
		t.Errorf("Path, Root = %q, %q; want %q, %q", l.Path, l.Root, path, root)
	}
	// The history path is resolved against the manifest's folder, not the
	// folder the program happens to run from.
	if want := filepath.Join(root, ".tester", "history.sqlite"); l.HistoryPath != want {
		t.Errorf("HistoryPath = %q; want %q", l.HistoryPath, want)
	}
	if len(l.Manifest.Tests) != 2 {
		t.Errorf("loaded %d tests; want 2", len(l.Manifest.Tests))
	}
}

func TestLoadReportsEveryProblemAndNamesTheFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	broken := strings.Replace(goodManifest, "project: p", "project: \"\"", 1)
	broken = strings.Replace(broken, "runner: sql, file: tests/b.sql", "runner: nope, file: tests/b.sql", 1)
	write(t, path, broken)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a broken manifest")
	}
	for _, want := range []string{path, "2 problems", "project: is required", `runner "nope" is not defined`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, err)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(filepath.Join(root, "missing.yaml")); err == nil {
		t.Error("Load of a missing file returned no error")
	}
	bad := filepath.Join(root, "bad.yaml")
	write(t, bad, "bogus: 1\n")
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("a parse error must name the file; got %v", err)
	}
}

func loaded(t *testing.T) *Loaded {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, FileName), goodManifest)
	l, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestCheckFilesAllPresent(t *testing.T) {
	l := loaded(t)
	write(t, filepath.Join(l.Root, "tests", "a.sql"), "select 1;")
	write(t, filepath.Join(l.Root, "tests", "b.sql"), "select 2;")
	if errs := CheckFiles(l); len(errs) != 0 {
		t.Errorf("unexpected problems: %v", errs)
	}
}

func TestCheckFilesReportsEveryProblem(t *testing.T) {
	l := loaded(t)
	// tests/a.sql is missing; tests/b.sql is a folder, not a file.
	if err := os.MkdirAll(filepath.Join(l.Root, "tests", "b.sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	errs := CheckFiles(l)
	if len(errs) != 2 {
		t.Fatalf("got %d problems; want 2: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "tests[0] (one)") || !strings.Contains(errs[0].Error(), "does not exist") {
		t.Errorf("first problem = %v", errs[0])
	}
	if !strings.Contains(errs[1].Error(), "tests[1] (two)") || !strings.Contains(errs[1].Error(), "not a regular file") {
		t.Errorf("second problem = %v", errs[1])
	}
}

func TestCheckFilesSymlinks(t *testing.T) {
	l := loaded(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "x.sql"), "select 'elsewhere';")
	write(t, filepath.Join(l.Root, "tests", "real.sql"), "select 1;")

	// tests/a.sql points outside the project; tests/b.sql points inside it.
	if err := os.Symlink(filepath.Join(outside, "x.sql"), filepath.Join(l.Root, "tests", "a.sql")); err != nil {
		t.Skipf("symbolic links are not available: %v", err)
	}
	if err := os.Symlink(filepath.Join(l.Root, "tests", "real.sql"), filepath.Join(l.Root, "tests", "b.sql")); err != nil {
		t.Fatal(err)
	}

	errs := CheckFiles(l)
	if len(errs) != 1 {
		t.Fatalf("got %d problems; want exactly the escaping link: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "tests[0] (one)") || !strings.Contains(errs[0].Error(), "outside the project") {
		t.Errorf("problem = %v", errs[0])
	}
}
