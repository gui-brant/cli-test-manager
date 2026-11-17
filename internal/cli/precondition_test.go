package cli

import (
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/tree"
)

// withPrecondition is the two-environment project with a precondition on local.
var withPrecondition = strings.Replace(twoEnvironments,
	"  local:\n    vars: { BASE_URL: \"http://127.0.0.1:8000\" }\n",
	"  local:\n    vars: { BASE_URL: \"http://127.0.0.1:8000\" }\n"+
		"    preconditions:\n"+
		"      - name: migrations-applied\n"+
		"        check: [\"check-migrations\"]\n"+
		"        message: \"Run `db reset` first.\"\n", 1)

// migrationsAre scripts the precondition's check: ready or not.
func migrationsAre(p *project, ready *bool) {
	p.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if inv.Args[0] == "check-migrations" {
			if *ready {
				return core.Output{Stdout: []byte("all applied\n")}, nil
			}
			return core.Output{ExitCode: 1, Stdout: []byte("2 migrations are not applied\n")}, nil
		}
		return defaultReply(n, inv)
	}
}

func testsStarted(p *project) int {
	n := 0
	for _, c := range p.runner.calls {
		if c.Args[0] == "run-sql-tests" || c.Args[0] == "http-runner" {
			n++
		}
	}
	return n
}

func TestARefusedRunExitsTwoSaysWhatToDoAndIsRecorded(t *testing.T) {
	p := newProject(t, withPrecondition)
	ready := false
	migrationsAre(p, &ready)

	if code := p.do("run", "--env", "local", "--yes"); code != ExitError {
		t.Fatalf("exit %d; want %d (a push hook must stop)", code, ExitError)
	}
	contains(t, "stderr", p.stderr(),
		"nothing was run: 1 precondition is not met",
		"precondition migrations-applied is not met: Run `db reset` first.",
		"the check said: 2 migrations are not applied",
		"Recorded as run 1 (tester history 1). Do what it says, then run again.")
	if strings.Contains(p.stdout(), "[1/") || testsStarted(p) != 0 {
		t.Errorf("a test started:\n%s", p.stdout())
	}

	// The history shows that the run was asked for and why it did not start.
	if code := p.do("history"); code != ExitOK {
		t.Fatalf("history: exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "history", p.stdout(), "NOT RUN")
	if code := p.do("history", "1"); code != ExitOK {
		t.Fatalf("history 1: exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "history 1", p.stdout(), "not run", "migrations-applied", "Run `db reset` first.", "2 migrations are not applied")
}

func TestARefusedRunIsNeverAskedAbout(t *testing.T) {
	p := newProject(t, withPrecondition)
	ready := false
	migrationsAre(p, &ready)
	p.deps.IsTerminal = true // a person at a terminal, a run of every test, no --yes

	if code := p.do("run", "--env", "local"); code != ExitError {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	if strings.Contains(p.stderr(), "[y/N]") || strings.Contains(p.stderr(), "Are you sure") {
		t.Errorf("a run that cannot start was asked about:\n%s", p.stderr())
	}
	contains(t, "stderr", p.stderr(), "Run `db reset` first.")
}

func TestAHookRunThatCannotStartStopsThePush(t *testing.T) {
	p := newProject(t, withPrecondition)
	p.deps.Tree = tree.New()
	ready := false
	migrationsAre(p, &ready)
	if code := p.do("run", "--env", "local", "--trigger", "hook", "--unplayed"); code != ExitError {
		t.Fatalf("exit %d; the hook must see a non-zero code", code)
	}
	contains(t, "stderr", p.stderr(), "Run `db reset` first.")
}

func TestADryRunReportsARefusalAndSavesNothing(t *testing.T) {
	p := newProject(t, withPrecondition)
	ready := false
	migrationsAre(p, &ready)

	if code := p.do("run", "--env", "local", "--dry-run"); code != ExitError {
		t.Fatalf("exit %d; a dry run of a run that would not start is a %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(),
		"a run would not start: 1 precondition is not met",
		"Run `db reset` first.", "Dry run: nothing was started or saved.")
	if strings.Contains(p.stdout(), "would run") {
		t.Errorf("a dry run listed tests it would refuse to run:\n%s", p.stdout())
	}
	if p.historyExists() || testsStarted(p) != 0 {
		t.Errorf("history created %v, %d tests started", p.historyExists(), testsStarted(p))
	}
}

func TestAMetPreconditionChangesNothingForThePerson(t *testing.T) {
	p := newProject(t, withPrecondition)
	ready := true
	migrationsAre(p, &ready)

	if code := p.do("run", "--env", "local", "--yes"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "[1/3] users-table ... ok", "Run 1: PASSED")
	if p.stderr() != "" {
		t.Errorf("stderr should be empty:\n%s", p.stderr())
	}
}

func TestRunningAgainAfterDoingWhatItSaidWorks(t *testing.T) {
	p := newProject(t, withPrecondition)
	ready := false
	migrationsAre(p, &ready)
	if code := p.do("run", "--env", "local", "--yes"); code != ExitError {
		t.Fatalf("first run: exit %d", code)
	}

	ready = true // the person ran `db reset`
	if code := p.do("run", "--env", "local", "--yes"); code != ExitOK {
		t.Fatalf("second run: exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "Run 2: PASSED")

	// Both stay in the history, newest first, and only the second has results.
	p.do("history")
	out := p.stdout()
	contains(t, "history", out, "NOT RUN", "PASS")
	if strings.Index(out, "PASS") > strings.Index(out, "NOT RUN") {
		t.Errorf("the newer run should be listed first:\n%s", out)
	}
	// The run that was not made played no test.
	p.do("counts", "--by", "test")
	contains(t, "counts", p.stdout(), "users-table")
}

func TestARefusalCreatesTheHistoryFile(t *testing.T) {
	// A refusal is recorded like any run, so the person finds it with
	// `tester history` the first time they look.
	p := newProject(t, withPrecondition)
	ready := false
	migrationsAre(p, &ready)
	p.do("run", "--env", "local", "--yes")
	if !p.historyExists() {
		t.Error("the refusal was not recorded: no history file")
	}
}
