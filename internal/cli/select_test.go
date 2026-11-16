package cli

import (
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/tree"
)

func commandsStarted(p *project) int { return len(p.runner.calls) }

func TestChangedFilesRunOnlyTheFeaturesTheySelect(t *testing.T) {
	p := newProject(t, snapshotManifest) // accounts: db/**/*.sql   notes: docs/**
	p.deps.Tree = tree.New()

	if code := p.do("run", "--env", "local", "--changed", "db/migrations/001.sql", "--yes"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	out := p.stdout()
	contains(t, "stdout", out, "Changed files: 1; they select: accounts",
		"[1/2] users-table ... ok", "[2/2] login-flow ... ok", "Run 1: PASSED")
	if strings.Contains(out, "note-create") {
		t.Errorf("a test of a feature that was not selected ran:\n%s", out)
	}

	// Repeating the flag, and a file in no feature.
	p.do("run", "--env", "local", "--changed", "docs/a.md", "--changed", "README.md", "--yes")
	contains(t, "stdout", p.stdout(), "Changed files: 2 (1 in no feature, so general); they select: general, notes", "note-create ... ok")
}

func TestChangedPathsCanComeFromStandardInput(t *testing.T) {
	p := newProject(t, snapshotManifest)
	p.deps.Tree = tree.New()
	p.deps.Stdin = strings.NewReader("docs/a.md\n\n  tests/http/login_flow.http  \n../outside.txt\n")

	if code := p.do("run", "--env", "local", "--changed", "-", "--yes"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	// A changed file selects a feature, and a selected feature runs whole: the
	// changed test file selects accounts (users-table and login-flow), the docs
	// file selects notes. Running only the changed test is what --unplayed adds.
	contains(t, "stdout", p.stdout(),
		"Changed files: 2; they select: accounts, notes (1 path outside the project, ignored)",
		"[1/3] users-table ... ok", "[2/3] login-flow ... ok", "[3/3] note-create ... ok")
}

func TestTheManifestInTheChangedFilesSelectsEveryFeature(t *testing.T) {
	p := newProject(t, snapshotManifest)
	p.deps.Tree = tree.New()
	p.do("run", "--env", "local", "--changed", "test-manifest.yaml", "--yes")
	contains(t, "stdout", p.stdout(), "including the manifest, which selects every feature", "[1/3] users-table", "[3/3] note-create")
}

// Nothing changed is a success that starts nothing, saves nothing and creates no
// history file: that is what lets a push hook pass.
func TestNothingChangedExitsZeroAndSavesNothing(t *testing.T) {
	for name, stdin := range map[string]string{"empty input": "", "only blank lines": "\n \n", "only outside": "../x\n/etc/passwd\n"} {
		p := newProject(t, snapshotManifest)
		p.deps.Tree = tree.New()
		p.deps.Stdin = strings.NewReader(stdin)
		if code := p.do("run", "--env", "local", "--trigger", "hook", "--changed", "-"); code != ExitOK {
			t.Fatalf("%s: exit %d; stderr: %s", name, code, p.stderr())
		}
		contains(t, name, p.stdout(), "they select no feature", "Nothing to run: no changed file selects a feature.")
		if p.historyExists() || commandsStarted(p) != 0 {
			t.Errorf("%s: history created %v, %d commands started", name, p.historyExists(), commandsStarted(p))
		}
	}
}

// A run narrowed by --changed or --unplayed is not "every test", so a person at a
// terminal is not asked.
func TestANarrowedRunDoesNotAsk(t *testing.T) {
	for name, args := range map[string][]string{
		"changed":  {"run", "--env", "local", "--changed", "db/a.sql"},
		"unplayed": {"run", "--env", "local", "--unplayed"},
	} {
		p := newProject(t, snapshotManifest)
		p.deps.Tree = tree.New()
		p.deps.IsTerminal = true
		if code := p.do(args...); code != ExitOK {
			t.Fatalf("%s: exit %d; stderr: %s", name, code, p.stderr())
		}
		if strings.Contains(p.stderr(), "[y/N]") {
			t.Errorf("%s: a question was asked:\n%s", name, p.stderr())
		}
	}
}

func TestUnplayedRunsEverythingTheFirstTimeAndNothingTheSecond(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.Tree = tree.New()

	if code := p.do("run", "--env", "local", "--unplayed", "--trigger", "hook"); code != ExitOK {
		t.Fatalf("first run: exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "first", p.stdout(), "[1/3] users-table ... ok", "[3/3] note-create ... ok", "Run 1: PASSED")

	p.runner.calls = nil
	if code := p.do("run", "--env", "local", "--unplayed", "--trigger", "hook"); code != ExitOK {
		t.Fatalf("second run: exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "second", p.stdout(),
		"skipped users-table (already passed in local with this file (run 1))",
		"skipped login-flow (already passed in local with this file (run 1))",
		"skipped note-create (already passed in local with this file (run 1))",
		"Nothing to run: every selected test has already passed in local with its current file.")
	if commandsStarted(p) != 0 {
		t.Errorf("%d commands started for tests that had already passed", commandsStarted(p))
	}
	p.do("history")
	if n := strings.Count(p.stdout(), "\n1 ") + strings.Count(p.stdout(), "\n2 "); n != 1 {
		t.Errorf("a second run was saved:\n%s", p.stdout())
	}
}

func TestUnplayedRunsATestWhoseFileChanged(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.Tree = tree.New()
	p.do("run", "--env", "local", "--yes")

	put(t, p, "tests/http/login_flow.http", "GET /login/v2\n")
	if code := p.do("run", "--env", "local", "--unplayed"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	out := p.stdout()
	contains(t, "stdout", out, "[1/1] login-flow ... ok", "Run 2: PASSED",
		"skipped users-table (already passed in local with this file (run 1))")
	if strings.Contains(out, "skipped login-flow") {
		t.Errorf("the changed test was left out:\n%s", out)
	}
}

func TestUnplayedRunsATestThatFailedBefore(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.Tree = tree.New()
	p.runner.reply = func(_ int, inv core.Invocation) (core.Output, error) {
		if inv.Args[0] == "http-runner" {
			return core.Output{Report: []byte(failingHTTP)}, nil
		}
		return defaultReply(0, inv)
	}
	if code := p.do("run", "--env", "local", "--yes"); code != ExitFailed {
		t.Fatalf("the first run should fail: exit %d", code)
	}
	p.runner.reply = defaultReply

	if code := p.do("run", "--env", "local", "--unplayed"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	// users-table passed; the two http tests failed, so they run again.
	contains(t, "stdout", p.stdout(), "skipped users-table", "[1/2] login-flow ... ok", "[2/2] note-create ... ok", "Run 2: PASSED")
}

func TestChangedAndUnplayedTogetherThroughTheCommand(t *testing.T) {
	p := newProject(t, snapshotManifest)
	p.deps.Tree = tree.New()
	p.do("run", "--env", "local", "--yes")
	put(t, p, "tests/http/note_create.http", "POST /notes/v2\n")

	// A change in docs selects notes; its test changed too, so it runs.
	p.do("run", "--env", "local", "--changed", "docs/a.md", "--unplayed")
	contains(t, "stdout", p.stdout(), "they select: notes", "[1/1] note-create ... ok")

	// A change in the accounts migrations: its tests already passed.
	p.runner.calls = nil
	if code := p.do("run", "--env", "local", "--changed", "db/a.sql", "--unplayed"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "they select: accounts", "Nothing to run: every selected test has already passed in local")
	if commandsStarted(p) != 0 {
		t.Errorf("%d commands started", commandsStarted(p))
	}
}

func TestUnplayedNeedsToReadTheTestFilesAndSaysSoBeforeAnything(t *testing.T) {
	p := newProject(t, twoEnvironments) // no Tree
	if code := p.do("run", "--env", "local", "--unplayed"); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "needs to read the test files")
	if p.historyExists() || commandsStarted(p) != 0 {
		t.Errorf("history created %v, %d commands started", p.historyExists(), commandsStarted(p))
	}
}

// The environment decides what has passed: the same tests are unplayed in an
// environment where they never passed.
func TestUnplayedIsPerEnvironment(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.Tree = tree.New()
	p.do("run", "--env", "local", "--yes")
	p.runner.calls = nil

	p.do("run", "--env", "deployed", "--unplayed")
	// users-table cannot run in deployed; the two http tests have not passed there.
	contains(t, "stdout", p.stdout(), "skipped users-table", "runs only in", "[1/2] login-flow ... ok", "[2/2] note-create ... ok")
}

// A dry run says what would run, with a line a script can look for, and changes
// nothing: no command, no history file, no question.
func TestDryRunSaysWhatWouldRunAndChangesNothing(t *testing.T) {
	p := newProject(t, snapshotManifest)
	p.deps.Tree = tree.New()
	p.deps.IsTerminal = true // and still nothing is asked

	if code := p.do("run", "--env", "local", "--changed", "db/a.sql", "--dry-run"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "Changed files: 1; they select: accounts",
		"would run users-table", "would run login-flow", "Dry run: 2 tests would run in local; nothing was started or saved.")
	// The line a script looks for keeps its two spaces: the helper above squashes them.
	if !strings.Contains(p.out.String(), "\nwould run  users-table\n") {
		t.Errorf("the raw line is not 'would run  <id>':\n%q", p.out.String())
	}
	if strings.Contains(p.stdout(), "note-create") || strings.Contains(p.stdout(), "[1/") {
		t.Errorf("a test of another feature, or a run, appeared:\n%s", p.stdout())
	}
	if p.historyExists() || commandsStarted(p) != 0 || strings.Contains(p.stderr(), "[y/N]") {
		t.Errorf("history created %v, %d commands started, stderr %q", p.historyExists(), commandsStarted(p), p.stderr())
	}
}

func TestDryRunListsWhatWasLeftOutAndIsSilentWhenThereIsNothing(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.Tree = tree.New()
	p.do("run", "--env", "local", "--yes")
	put(t, p, "tests/http/login_flow.http", "GET /login/v2\n")
	p.runner.calls = nil

	p.do("run", "--env", "local", "--unplayed", "--dry-run")
	contains(t, "dry run", p.stdout(), "skipped users-table (already passed in local with this file (run 1))",
		"would run login-flow", "Dry run: 1 test would run in local")
	if commandsStarted(p) != 0 {
		t.Errorf("%d commands started", commandsStarted(p))
	}

	// Nothing to run: no "would run" line at all, and still exit 0.
	put(t, p, "tests/http/login_flow.http", "GET /login\n") // back to the version that passed
	if code := p.do("run", "--env", "local", "--unplayed", "--dry-run"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(p.stdout(), "would run") || !strings.Contains(p.stdout(), "Nothing to run") {
		t.Errorf("a dry run with nothing to run:\n%s", p.stdout())
	}
	p.do("history")
	if n := strings.Count(p.stdout(), "\n1 ") + strings.Count(p.stdout(), "\n2 "); n != 1 {
		t.Errorf("a dry run saved a run:\n%s", p.stdout())
	}
}

func TestADryRunOfARequestThatCannotRunStillFails(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("run", "--env", "nowhere", "--dry-run"); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	if code := p.do("run", "--env", "staging", "--dry-run"); code != ExitError {
		t.Errorf("a missing secret: exit %d; want %d", code, ExitError)
	}
}

func TestHelpExplainsChangedAndUnplayed(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("run", "-h"); code != ExitOK {
		t.Errorf("exit %d", code)
	}
	contains(t, "run -h", p.stderr()+p.stdout(),
		"--changed PATH", "write --changed - to read one path per line", "the manifest selects every feature",
		"--unplayed", "already passed in this", "that is a success: it says so, exits 0, and saves nothing")
}
