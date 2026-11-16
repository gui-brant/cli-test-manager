package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/tree"
)

// snapshotManifest gives the accounts feature the path patterns that find its
// migrations, and the notes feature its docs.
var snapshotManifest = strings.Replace(twoEnvironments, "  accounts: {}\n  notes: {}",
	"  accounts:\n    paths: [\"db/**/*.sql\"]\n  notes:\n    paths: [\"docs/**\"]", 1)

// listTree reads files from disk like the real tree but lists a fixed set, as
// git would, or says it cannot list them.
type listTree struct {
	core.Tree
	files []string
	ok    bool
}

func (l listTree) Files(context.Context, string) ([]string, bool) { return l.files, l.ok }

func put(t *testing.T, p *project, rel, content string) {
	t.Helper()
	full := filepath.Join(p.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func matches(t *testing.T, what, text, pattern string) {
	t.Helper()
	if ok, err := regexp.MatchString(pattern, text); err != nil || !ok {
		t.Errorf("%s does not match %q:\n%s", what, pattern, text)
	}
}

func TestARunRecordsTheFilesItWasMadeWith(t *testing.T) {
	p := newProject(t, snapshotManifest)
	put(t, p, "db/migrations/001_a.sql", "create table a ();")
	put(t, p, "db/migrations/002_b.txt", "not a .sql file")
	put(t, p, "docs/readme.md", "belongs to the notes feature")
	p.deps.Tree = listTree{Tree: tree.New(), ok: true, files: []string{
		"db/migrations/001_a.sql", "db/migrations/002_b.txt", "docs/readme.md", "tests/http/login_flow.http",
	}}

	if code := p.do("run", "--env", "local", "--feature", "accounts", "--yes"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	if code := p.do("history", "1", "--files"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	out := p.stdout()
	// The manifest, the files of the two accounts tests, and the one project file
	// the accounts patterns match: not the notes feature's docs, not a .txt.
	contains(t, "files", out, "Files (4):", "Print one with: tester history 1 --file <path>")
	matches(t, "migration", out, `db/migrations/001_a\.sql +18 B [0-9a-f]{12} feature`)
	matches(t, "manifest", out, `test-manifest\.yaml +\d+ B [0-9a-f]{12} manifest`)
	matches(t, "http test", out, `tests/http/login_flow\.http +\d+ B [0-9a-f]{12} test login-flow`)
	matches(t, "sql test", out, `tests/sql/users_table\.sql +\d+ B [0-9a-f]{12} test users-table`)
	for _, not := range []string{"002_b.txt", "docs/readme.md", "note_create"} {
		if strings.Contains(out, not) {
			t.Errorf("%s was recorded:\n%s", not, out)
		}
	}
}

func TestARecordedFileIsPrintedAsItWasEvenAfterItChanged(t *testing.T) {
	p := newProject(t, snapshotManifest)
	put(t, p, "db/migrations/001_a.sql", "create table a ();")
	p.deps.Tree = listTree{Tree: tree.New(), ok: true, files: []string{"db/migrations/001_a.sql"}}
	p.do("run", "--env", "local", "--feature", "accounts", "--yes")

	put(t, p, "db/migrations/001_a.sql", "CHANGED AFTER THE RUN")
	if code := p.do("history", "1", "--file", "db/migrations/001_a.sql"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	out := p.stdout()
	contains(t, "file", out, "file: db/migrations/001_a.sql (feature)", "snapshot: run 1, commit not recorded", "create table a ();")
	if strings.Contains(out, "CHANGED") {
		t.Errorf("the live file is shown:\n%s", out)
	}

	// The manifest, exactly as it was read.
	p.do("history", "1", "--file", "test-manifest.yaml")
	contains(t, "manifest", p.stdout(), "(manifest)", "manifest_version: 2", "project: example")

	if code := p.do("history", "1", "--file", "nope.sql"); code != ExitError {
		t.Errorf("a path the run does not have: exit %d", code)
	}
	contains(t, "stderr", p.stderr(), `run 1 has no file "nope.sql" (see tester history 1 --files)`)
}

func TestTestsShowIsTheLatestRunsFileAndRunPicksAnother(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.Tree = tree.New()
	put(t, p, "tests/http/login_flow.http", "GET /login\n")
	p.do("run", "--env", "local", "--test", "login-flow", "--yes")
	put(t, p, "tests/http/login_flow.http", "GET /login/v2\n")
	p.do("run", "--env", "local", "--test", "login-flow", "--yes")
	put(t, p, "tests/http/login_flow.http", "GET /login/UNRECORDED\n")

	p.do("tests", "show", "login-flow")
	out := p.stdout()
	contains(t, "latest", out, "snapshot: run 2", "GET /login/v2")
	if strings.Contains(out, "UNRECORDED") {
		t.Errorf("the live file is shown:\n%s", out)
	}

	for _, args := range [][]string{{"tests", "show", "--run", "1", "login-flow"}, {"tests", "show", "login-flow", "--run", "1"}} {
		if code := p.do(args...); code != ExitOK {
			t.Fatalf("%v: exit %d; %s", args, code, p.stderr())
		}
		contains(t, "run 1", p.stdout(), "snapshot: run 1", "GET /login")
		if strings.Contains(p.stdout(), "v2") {
			t.Errorf("%v shows the later text:\n%s", args, p.stdout())
		}
	}

	// A test that never ran has none; and a run that did not play it has none.
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"never run":       {[]string{"tests", "show", "note-create"}, `test "note-create" has not been run by the tester yet, so there is no snapshot of its file: run it once`},
		"not in that run": {[]string{"tests", "show", "note-create", "--run", "1"}, `run 1 did not record a file for test "note-create"`},
		"no such run":     {[]string{"tests", "show", "login-flow", "--run", "99"}, "run 99 not found"},
		"a negative run":  {[]string{"tests", "show", "login-flow", "--run", "-3"}, "--run must be a run number"},
		"an unknown test": {[]string{"tests", "show", "nope"}, `test "nope" is not in the manifest`},
		"two ids":         {[]string{"tests", "show", "login-flow", "note-create"}, "unexpected argument"},
	} {
		if code := p.do(c.args...); code != ExitError {
			t.Errorf("%s: exit %d; want %d", name, code, ExitError)
		}
		contains(t, name, p.stderr(), c.want)
	}

	p.do("tests", "show", "-h")
	contains(t, "tests show -h", p.stderr()+p.stdout(), "never shows the file on disk", "--run")
}

func TestATestThatNeverRanHasNoSnapshotAndLookingCreatesNoHistory(t *testing.T) {
	p := newProject(t, twoEnvironments) // no history file yet
	if code := p.do("tests", "show", "login-flow"); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "has not been run by the tester yet")
	if p.historyExists() {
		t.Error("looking for a snapshot created the history file")
	}
}

// What cannot be kept is listed with the reason, and a secret never reaches the
// history, in the text or in the file.
func TestFilesWhoseTextCannotBeKeptAreListedWithTheReason(t *testing.T) {
	p := newProject(t, snapshotManifest)
	put(t, p, "docs/token.txt", "token=pw-12345-abc")
	put(t, p, "docs/bin.dat", "a\x00b")
	put(t, p, "docs/big.txt", strings.Repeat("x", 1<<20+1))
	put(t, p, "docs/ok.txt", "fine")
	p.deps.Creds = fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc"}
	p.deps.Tree = listTree{Tree: tree.New(), ok: true, files: []string{
		"docs/token.txt", "docs/bin.dat", "docs/big.txt", "docs/ok.txt", "docs/gone.txt",
	}}

	if code := p.do("run", "--env", "deployed", "--feature", "notes", "--yes"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	p.do("history", "1", "--files")
	out := p.stdout()
	matches(t, "secret", out, `docs/token\.txt +18 B [0-9a-f]{12} feature \(not kept: contains a secret value\)`)
	matches(t, "binary", out, `docs/bin\.dat +3 B [0-9a-f]{12} feature \(not kept: binary\)`)
	matches(t, "large", out, `docs/big\.txt +1048577 B [0-9a-f]{12} feature \(not kept: larger than 1 MiB\)`)
	matches(t, "unreadable", out, `docs/gone\.txt +0 B - feature \(not kept: could not be read: no such file or directory\)`)
	if ok, _ := regexp.MatchString(`docs/ok\.txt +4 B [0-9a-f]{12} feature\n`, out+"\n"); !ok {
		t.Errorf("a plain file is not listed as kept:\n%s", out)
	}

	p.do("history", "1", "--file", "docs/token.txt")
	if out := p.stdout(); !strings.Contains(out, "The text of docs/token.txt was not kept: contains a secret value.") || strings.Contains(out, "pw-12345-abc") {
		t.Errorf("a file with a secret:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(p.root, ".tester", "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pw-12345-abc") {
		t.Error("the secret is in the history file")
	}
}

// Without a list of the project's files only what is known by name is kept, and
// the run says so. Without any way to read files, only the manifest is.
func TestWithoutAListOfFilesOnlyTheManifestAndTheTestsAreRecorded(t *testing.T) {
	p := newProject(t, snapshotManifest)
	put(t, p, "db/migrations/001_a.sql", "create table a ();")
	p.deps.Tree = listTree{Tree: tree.New(), ok: false}

	if code := p.do("run", "--env", "local", "--feature", "accounts", "--yes"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "run", p.stdout(), "Note: the project's files could not be listed (is it under git?), so only the manifest and the tests' files were recorded")
	p.do("history", "1", "--files")
	contains(t, "files", p.stdout(), "Files (3):", "test-manifest.yaml", "tests/http/login_flow.http", "tests/sql/users_table.sql")
	if strings.Contains(p.stdout(), "db/migrations") {
		t.Errorf("a project file was recorded without a list:\n%s", p.stdout())
	}

	q := newProject(t, snapshotManifest) // no tree at all
	q.do("run", "--env", "local", "--feature", "accounts", "--yes")
	q.do("history", "1", "--files")
	contains(t, "files", q.stdout(), "Files (1):", "test-manifest.yaml")
}

func TestPulledAndImportedRunsHaveNoFiles(t *testing.T) {
	p := newProject(t, importManifest)
	p.deps.Tree = tree.New()
	if code := p.do("import", "--env", "network", resultFile(t, "r.xml", resultsXML)); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stderr())
	}
	if code := p.do("history", "1", "--files"); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stderr())
	}
	contains(t, "files", p.stdout(), "none were recorded")
	if strings.Contains(p.stdout(), "Print one with") {
		t.Errorf("an empty list offers to print a file:\n%s", p.stdout())
	}
}

func TestFilesAndFileNeedARunNumber(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.do("run", "--env", "local", "--feature", "notes", "--yes")
	cases := map[string]struct {
		args []string
		want string
	}{
		"--files alone":      {[]string{"history", "--files"}, "--files and --file show one run's files"},
		"--file alone":       {[]string{"history", "--file", "x"}, "--files and --file show one run's files"},
		"both":               {[]string{"history", "1", "--files", "--file", "x"}, "not both"},
		"a run that is not":  {[]string{"history", "9", "--files"}, "run 9 not found"},
		"not a number":       {[]string{"history", "abc", "--files"}, `"abc" is not a run number`},
		"a missing run file": {[]string{"history", "9", "--file", "x"}, "run 9 not found"},
	}
	for name, c := range cases {
		if code := p.do(c.args...); code != ExitError {
			t.Errorf("%s: exit %d; want %d", name, code, ExitError)
		}
		contains(t, name, p.stderr(), c.want)
	}
	p.do("history", "-h")
	contains(t, "history -h", p.stderr()+p.stdout(), "--files", "--file <path>", "pulled or imported run happened elsewhere")
}
