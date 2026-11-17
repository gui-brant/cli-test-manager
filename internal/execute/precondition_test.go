package execute

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// ---- helpers ---------------------------------------------------------------

var (
	migrationsApplied = core.Precondition{
		Name: "migrations-applied", Check: []string{"check-migrations"},
		Message: "Run `db reset`: the database is behind the migration files.",
	}
	functionServed = core.Precondition{
		Name: "function-served", Check: []string{"check-function"},
		Message: "Run `serve add-file-object` in another terminal.",
	}
)

// checks scripts the answers of the precondition checks (commands named
// check-...). Every other command gets the usual reply.
func (f *fixture) checks(answers map[string]core.Output) {
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if o, ok := answers[inv.Args[0]]; ok {
			return o, nil
		}
		return defaultReply(n, inv)
	}
}

// checkCalls lists, in order, the check commands that were started.
func (f *fixture) checkCalls() []string {
	var names []string
	for _, c := range f.runner.calls {
		if strings.HasPrefix(c.Args[0], "check-") {
			names = append(names, c.Args[0])
		}
	}
	return names
}

// testCalls counts the commands that run tests, which no refusal may start.
func (f *fixture) testCalls() int {
	n := 0
	for _, c := range f.runner.calls {
		if c.Args[0] == "run-sql-tests" || c.Args[0] == "http-runner" {
			n++
		}
	}
	return n
}

func (f *fixture) preconditionsOn(env string, ps ...core.Precondition) {
	spec := f.manifest.Environments[env]
	spec.Preconditions = ps
	f.manifest.Environments[env] = spec
}

func (f *fixture) preconditionsOnRunner(name string, ps ...core.Precondition) {
	spec := f.manifest.Runners[name]
	spec.Preconditions = ps
	f.manifest.Runners[name] = spec
}

var unmetMigrations = core.Output{ExitCode: 1, Stdout: []byte("2 migrations are not applied\nsecond line\n")}

// ---- tests -----------------------------------------------------------------

func TestAnUnmetPreconditionStartsNothingAndSavesARefusedRun(t *testing.T) {
	f := newFixture(t)
	f.svc.Checkout = &fakeCheckout{commit: "abc123", dirty: true}
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})

	rep, err := f.run("local", Selection{})
	if err != nil {
		t.Fatalf("a refusal is not an error: %v", err)
	}
	if len(rep.Refused) != 1 || rep.Refused[0].Name != "migrations-applied" {
		t.Fatalf("refused = %+v; want the one precondition", rep.Refused)
	}
	if rep.Refused[0].Why != "2 migrations are not applied" {
		t.Errorf("why = %q; want the first line the check printed", rep.Refused[0].Why)
	}
	if n := f.testCalls(); n != 0 {
		t.Errorf("%d test commands started; a refused run starts none", n)
	}
	if len(f.events) != 0 {
		t.Errorf("%d progress events; a refused run reports none", len(f.events))
	}

	if !rep.Saved || rep.RunID == 0 || f.storedRuns() != 1 {
		t.Fatalf("saved %v, id %d, %d stored; want the refusal recorded once", rep.Saved, rep.RunID, f.storedRuns())
	}
	got, err := f.store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.RunRefused || len(got.Results) != 0 || got.Environment != "local" || got.Source != core.SourceLocal {
		t.Errorf("stored run: state %q, %d results, env %q, source %q", got.State, len(got.Results), got.Environment, got.Source)
	}
	if got.CommitSHA != "abc123" || !got.Dirty || got.Trigger != core.TriggerManual {
		t.Errorf("stored run: commit %q, dirty %v, trigger %q; want abc123, true, manual", got.CommitSHA, got.Dirty, got.Trigger)
	}
	for _, want := range []string{"migrations-applied", "Run `db reset`", "2 migrations are not applied"} {
		if !strings.Contains(got.Diagnostic, want) {
			t.Errorf("diagnostic %q should contain %q", got.Diagnostic, want)
		}
	}
	if v := got.Verdict(); v.Outcome != core.OutcomeUndetermined || !strings.Contains(v.Reason, "not run") {
		t.Errorf("verdict %+v; a refused run is neither a pass nor a fail, and says it was not run", v)
	}
}

func TestARefusedRunKeepsWhatStartedIt(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})
	rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Trigger: core.TriggerHook})
	if err != nil || !rep.Saved {
		t.Fatalf("run: %v, saved %v", err, rep.Saved)
	}
	got, err := f.store.GetRun(bg, rep.RunID)
	if err != nil || got.Trigger != core.TriggerHook {
		t.Errorf("trigger = %q (%v); want hook, so the history shows a push was stopped", got.Trigger, err)
	}
}

func TestEveryUnmetPreconditionIsListedTogether(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.preconditionsOnRunner("http", functionServed)
	f.manifest.Tests[2].Preconditions = []core.Precondition{{Name: "seed-loaded", Check: []string{"check-seed"}, Message: "Run the seed."}}
	f.checks(map[string]core.Output{
		"check-migrations": unmetMigrations,
		"check-function":   {ExitCode: 7, Stderr: []byte("nothing is listening on 54321\n")},
		"check-seed":       {ExitCode: 1},
	})

	rep := f.mustRun("local", Selection{})
	var names []string
	for _, r := range rep.Refused {
		names = append(names, r.Name)
	}
	if want := "migrations-applied function-served seed-loaded"; strings.Join(names, " ") != want {
		t.Fatalf("refused %v; want %q: the environment's, then the runner's, then the test's, all at once", names, want)
	}
	if rep.Refused[1].Why != "nothing is listening on 54321" {
		t.Errorf("a check's error output is used when it printed nothing else; got %q", rep.Refused[1].Why)
	}
	if !strings.Contains(rep.Refused[2].Why, "exited with code 1 and printed nothing") {
		t.Errorf("a silent check says so; got %q", rep.Refused[2].Why)
	}
	got, _ := f.store.GetRun(bg, rep.RunID)
	if strings.Count(got.Diagnostic, "is not met") != 3 {
		t.Errorf("the saved reason should name all three:\n%s", got.Diagnostic)
	}
	if f.testCalls() != 0 {
		t.Error("tests started")
	}
}

func TestMetPreconditionsChangeNothing(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": {ExitCode: 0, Stdout: []byte("all applied\n")}})

	rep := f.mustRun("local", Selection{})
	if len(rep.Refused) != 0 || !rep.Saved {
		t.Fatalf("refused %v, saved %v; met preconditions must not stop the run", rep.Refused, rep.Saved)
	}
	got, _ := f.store.GetRun(bg, rep.RunID)
	if got.State != core.RunComplete || len(got.Results) == 0 {
		t.Errorf("state %q, %d results; want a normal complete run", got.State, len(got.Results))
	}
	if f.testCalls() == 0 {
		t.Error("no test started")
	}
	// A check gets exactly the environment the tests get, in the project folder.
	for _, c := range f.runner.calls {
		if c.Args[0] != "check-migrations" {
			continue
		}
		if c.Dir != "/project" || c.Timeout != preconditionTimeout {
			t.Errorf("check ran in %q with timeout %v; want /project and %v", c.Dir, c.Timeout, preconditionTimeout)
		}
		if !strings.Contains(strings.Join(c.Env, "\n"), "BASE_URL=http://127.0.0.1:8000") {
			t.Errorf("check environment %v lacks the environment's variables", c.Env)
		}
	}
}

func TestAPreconditionNamedInSeveralPlacesIsCheckedOnce(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.preconditionsOnRunner("sql", migrationsApplied)
	f.manifest.Tests[0].Preconditions = []core.Precondition{migrationsApplied}
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})

	rep := f.mustRun("local", Selection{})
	if got := f.checkCalls(); len(got) != 1 {
		t.Errorf("checks started: %v; want it once", got)
	}
	if len(rep.Refused) != 1 {
		t.Errorf("%d refusals; want one, not three copies of the message", len(rep.Refused))
	}
}

func TestPreconditionsOfTestsThatDoNotRunAreNotChecked(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOnRunner("http", functionServed)
	f.manifest.Tests[2].Preconditions = []core.Precondition{{Name: "seed-loaded", Check: []string{"check-seed"}, Message: "Run the seed."}}
	f.checks(map[string]core.Output{"check-function": {ExitCode: 1}, "check-seed": {ExitCode: 1}})

	// Only the sql test runs: neither the http runner's nor note-create's precondition applies.
	rep := f.mustRun("local", Selection{TestIDs: []string{"users-table"}})
	if len(rep.Refused) != 0 || len(f.checkCalls()) != 0 {
		t.Errorf("refused %v, checks %v; a precondition of a test that does not run is not checked", rep.Refused, f.checkCalls())
	}
	if f.testCalls() != 1 {
		t.Errorf("%d test commands; want the one test", f.testCalls())
	}

	// Selecting the test that has one checks it, and the runner's too.
	f = newFixture(t)
	f.preconditionsOnRunner("http", functionServed)
	f.manifest.Tests[2].Preconditions = []core.Precondition{{Name: "seed-loaded", Check: []string{"check-seed"}, Message: "Run the seed."}}
	f.checks(map[string]core.Output{"check-function": {ExitCode: 0}, "check-seed": {ExitCode: 1}})
	rep = f.mustRun("local", Selection{TestIDs: []string{"note-create"}})
	if len(rep.Refused) != 1 || rep.Refused[0].Name != "seed-loaded" {
		t.Errorf("refused %+v; want only seed-loaded (the runner's passed)", rep.Refused)
	}
}

func TestPreviewReportsARefusalAndSavesNothing(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})

	pv, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Refused) != 1 || len(pv.Tests) != 3 {
		t.Errorf("refused %d, tests %d; the preview shows what would run and that it would be refused", len(pv.Refused), len(pv.Tests))
	}
	if f.storedRuns() != 0 || f.testCalls() != 0 {
		t.Errorf("%d stored, %d test commands; a preview saves and starts nothing", f.storedRuns(), f.testCalls())
	}

	// With nothing to run there is nothing to refuse, so no check is started.
	f = newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})
	pv, err = f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{UseChanged: true}})
	if err != nil || len(pv.Refused) != 0 || len(f.checkCalls()) != 0 {
		t.Errorf("nothing selected: err %v, refused %v, checks %v", err, pv.Refused, f.checkCalls())
	}
}

func TestACheckThatTimesOutIsNotMet(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": {TimedOut: true, ExitCode: -1}})
	rep := f.mustRun("local", Selection{})
	if len(rep.Refused) != 1 || !strings.Contains(rep.Refused[0].Why, "timed out") {
		t.Errorf("refused %+v; a check that hangs is not met, and says it timed out", rep.Refused)
	}
}

func TestACheckThatCannotBeStartedIsAMistakeNotARefusal(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if inv.Args[0] == "check-migrations" {
			return core.Output{}, errors.New("runner: executable file not found")
		}
		return defaultReply(n, inv)
	}
	rep, err := f.run("local", Selection{})
	if err == nil || !strings.Contains(err.Error(), `precondition "migrations-applied"`) {
		t.Fatalf("error = %v; want one naming the precondition whose check could not run", err)
	}
	if len(rep.Refused) != 0 || f.storedRuns() != 0 || f.testCalls() != 0 {
		t.Errorf("refused %d, stored %d, tests %d; a broken check is a manifest problem, not a refused run", len(rep.Refused), f.storedRuns(), f.testCalls())
	}
}

func TestCancellingDuringACheckSavesNothing(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if inv.Args[0] == "check-migrations" {
			cancel()
			return core.Output{}, fmt.Errorf("runner: stopped: %w", context.Canceled)
		}
		return defaultReply(n, inv)
	}
	_, err := f.runWith(ctx, "local", Selection{})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "nothing was saved") {
		t.Errorf("error = %v; want a cancellation that says nothing was saved", err)
	}
	if f.storedRuns() != 0 {
		t.Error("a cancelled check left a run behind")
	}
}

func TestARefusedRunIsNotATestBeingPlayed(t *testing.T) {
	f := newFixture(t)
	f.svc.Tree = newTree(true, map[string]string{
		"tests/sql/users_table.sql":   "select 1;",
		"tests/http/login_flow.http":  "GET /login",
		"tests/http/note_create.http": "POST /notes",
	})
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})
	f.mustRun("local", Selection{})

	played, err := f.store.PassedTestFiles(bg, "local")
	if err != nil || len(played) != 0 {
		t.Fatalf("after a refusal %d tests count as passed (%v); want none", len(played), err)
	}

	// The person does what the message says, and asks for what was never played.
	f.checks(map[string]core.Output{"check-migrations": {ExitCode: 0}})
	rep := f.mustRun("local", Selection{Unplayed: true})
	if len(rep.Skipped) != 0 || len(rep.Run.Results) == 0 {
		t.Errorf("skipped %d, results %d; a refusal played no test, so every test is still unplayed", len(rep.Skipped), len(rep.Run.Results))
	}
}

func TestACheckCannotLeakASecretIntoTheHistory(t *testing.T) {
	f := newFixture(t)
	spec := f.manifest.Environments["deployed"]
	spec.Preconditions = []core.Precondition{{Name: "signed-in", Check: []string{"check-signin"}, Message: "Sign in first."}}
	f.manifest.Environments["deployed"] = spec
	f.checks(map[string]core.Output{"check-signin": {ExitCode: 1, Stdout: []byte("login refused for pw-12345-abc\n")}})

	rep := f.mustRun("deployed", Selection{})
	if len(rep.Refused) != 1 {
		t.Fatalf("refused %+v", rep.Refused)
	}
	got, _ := f.store.GetRun(bg, rep.RunID)
	for _, text := range []string{rep.Refused[0].Why, got.Diagnostic} {
		if strings.Contains(text, "pw-12345-abc") {
			t.Errorf("the secret reached %q", text)
		}
	}
}

func TestAFailingStoreStillReportsTheRefusal(t *testing.T) {
	f := newFixture(t)
	f.preconditionsOn("local", migrationsApplied)
	f.checks(map[string]core.Output{"check-migrations": unmetMigrations})
	f.svc.Store = failingStore{}
	rep, err := f.run("local", Selection{})
	if err == nil || !strings.Contains(err.Error(), "saving the refused run") {
		t.Fatalf("error = %v; want one saying the refusal could not be saved", err)
	}
	if len(rep.Refused) != 1 || rep.Saved {
		t.Errorf("refused %d, saved %v; the person must still be told what to do", len(rep.Refused), rep.Saved)
	}
}
