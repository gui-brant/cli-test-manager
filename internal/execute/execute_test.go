package execute

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

var (
	bg = context.Background()
	t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
)

// ---- fakes -----------------------------------------------------------------

// fakeRunner records every invocation and answers with a scripted reply.
type fakeRunner struct {
	calls []core.Invocation
	reply func(n int, inv core.Invocation) (core.Output, error)
}

func (f *fakeRunner) Run(_ context.Context, inv core.Invocation) (core.Output, error) {
	f.calls = append(f.calls, inv)
	return f.reply(len(f.calls), inv)
}

type fakeCreds map[string]string

func (c fakeCreds) Get(_ context.Context, name string) (string, error) {
	if v, ok := c[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("fake: %s: %w", name, core.ErrNotFound)
}

// fakeClock moves one second on at every reading.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time {
	t := c.now
	c.now = c.now.Add(time.Second)
	return t
}

// failingStore stands in for a store that cannot save.
type failingStore struct{ core.Store }

func (failingStore) SaveRun(context.Context, core.Run) (int64, error) {
	return 0, errors.New("disk is full")
}

func parentEnv() []string {
	return []string{
		"PATH=/usr/bin",
		"HOME=/home/x",
		"BASE_URL=http://parent-value",   // the environment sets its own, which wins
		"ACCOUNT_PASSWORD=parent-secret", // declared: must never be passed on
		"OTHER_PASSWORD=other-secret",    // declared by another environment
		"CI_TOKEN=ci-secret",             // the pull token
		"NOT_A_PAIR",
	}
}

func testManifest() *core.Manifest {
	return &core.Manifest{
		Version:  1,
		Project:  "example",
		History:  ".tester/history.sqlite",
		Features: []string{"accounts", "notes"},
		Environments: map[string]core.EnvironmentSpec{
			"local":    {Vars: map[string]string{"BASE_URL": "http://127.0.0.1:8000"}},
			"deployed": {Vars: map[string]string{"BASE_URL": "https://dev.example.test"}, Secrets: []string{"ACCOUNT_PASSWORD"}},
			"other":    {Secrets: []string{"OTHER_PASSWORD"}},
		},
		Runners: map[string]core.RunnerSpec{
			"sql": {
				Run: []string{"run-sql-tests", "{files}"}, Format: core.FormatTAP, Output: core.OutputStdout,
				Environments: []string{"local"}, Timeout: 10 * time.Minute,
			},
			"http": {
				Run: []string{"http-runner", "--report", "{report}", "{files}"}, Format: core.FormatJUnit, Output: core.OutputFile,
				Environments: []string{"local", "deployed"}, Timeout: 10 * time.Minute,
			},
		},
		Tests: []core.TestSpec{
			{ID: "users-table", Name: "Users table checks", Feature: "accounts", Runner: "sql", File: "tests/sql/users_table.sql"},
			{ID: "login-flow", Name: "Login flow", Feature: "accounts", Runner: "http", File: "tests/http/login_flow.http"},
			{ID: "note-create", Name: "Create a note", Feature: "notes", Runner: "http", File: "tests/http/note_create.http"},
		},
		Pull: &core.PullSpec{
			Provider: "github", Repo: "owner/name", Workflow: "tests.yml", Artifact: "test-results",
			Environment: "deployed", LookbackDays: 30, Token: "CI_TOKEN",
		},
	}
}

const (
	tapPass   = "1..2\nok 1 - first\nok 2 - second\n"
	junitPass = `<testsuite name="s"><testcase classname="c" name="signs in" time="0.5"/></testsuite>`
)

func defaultReply(_ int, inv core.Invocation) (core.Output, error) {
	switch inv.Args[0] {
	case "run-sql-tests":
		return core.Output{Stdout: []byte(tapPass)}, nil
	case "http-runner":
		return core.Output{Report: []byte(junitPass)}, nil
	}
	return core.Output{}, errors.New("unexpected command " + inv.Args[0])
}

// ---- fixture ---------------------------------------------------------------

type fixture struct {
	t        *testing.T
	svc      *Service
	runner   *fakeRunner
	store    *sqlite.Store
	manifest *core.Manifest
	events   []Event
}

// newFixture uses a REAL history file in a temporary folder, as decided: the
// service's tests check real behaviour.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	clock := &fakeClock{now: t0}
	f := &fixture{t: t, runner: &fakeRunner{reply: defaultReply}, store: store, manifest: testManifest()}
	f.svc = &Service{
		Runner:  f.runner,
		Creds:   fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc"},
		Store:   store,
		Environ: parentEnv,
		Now:     clock.Now,
	}
	return f
}

func (f *fixture) run(env string, sel Selection) (Report, error) {
	return f.runWith(bg, env, sel)
}

func (f *fixture) runWith(ctx context.Context, env string, sel Selection) (Report, error) {
	return f.svc.Run(ctx, Request{
		Manifest: f.manifest, Root: "/project", Environment: env, Select: sel,
		Progress: func(e Event) { f.events = append(f.events, e) },
	})
}

func (f *fixture) mustRun(env string, sel Selection) Report {
	f.t.Helper()
	rep, err := f.run(env, sel)
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return rep
}

func (f *fixture) storedRuns() int {
	f.t.Helper()
	runs, err := f.store.ListRuns(bg, core.RunFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	return len(runs)
}

func startedIDs(events []Event) []string {
	var ids []string
	for _, e := range events {
		if e.Kind == EventStarted {
			ids = append(ids, e.Test.ID)
		}
	}
	return ids
}

// ---- tests -----------------------------------------------------------------

func TestARunOfTwoTests(t *testing.T) {
	f := newFixture(t)
	rep := f.mustRun("local", Selection{Feature: "accounts"})

	if !rep.Saved || rep.RunID == 0 {
		t.Fatalf("Saved %v, RunID %d; want a saved run", rep.Saved, rep.RunID)
	}

	// One command per test, in manifest order, in the project folder.
	if len(f.runner.calls) != 2 {
		t.Fatalf("%d commands; want 2, one per test", len(f.runner.calls))
	}
	first, second := f.runner.calls[0], f.runner.calls[1]
	if want := []string{"run-sql-tests", "tests/sql/users_table.sql"}; !reflect.DeepEqual(first.Args, want) {
		t.Errorf("first command %v; want %v", first.Args, want)
	}
	// The report placeholder is left for the runner, which owns the file.
	if want := []string{"http-runner", "--report", "{report}", "tests/http/login_flow.http"}; !reflect.DeepEqual(second.Args, want) {
		t.Errorf("second command %v; want %v", second.Args, want)
	}
	for i, c := range f.runner.calls {
		if c.Dir != "/project" || c.Timeout != 10*time.Minute {
			t.Errorf("command %d: dir %q, timeout %v; want the project folder and the runner's timeout", i, c.Dir, c.Timeout)
		}
	}
	if first.WantsReport || !second.WantsReport {
		t.Errorf("WantsReport = %v, %v; want false for stdout output and true for file output", first.WantsReport, second.WantsReport)
	}

	run := rep.Run
	if run.Source != core.SourceLocal || run.Environment != "local" || run.State != core.RunComplete {
		t.Errorf("run header: %q %q %q", run.Source, run.Environment, run.State)
	}
	if !run.StartedAt.Equal(t0) || !run.FinishedAt.Equal(t0.Add(time.Second)) {
		t.Errorf("times %v to %v; want them from the clock", run.StartedAt, run.FinishedAt)
	}
	if run.ExitCode != 0 || run.Diagnostic != "" {
		t.Errorf("exit code %d, diagnostic %q; want a clean run", run.ExitCode, run.Diagnostic)
	}
	if v := run.Verdict(); v.Outcome != core.OutcomePass {
		t.Errorf("verdict %+v; want pass", v)
	}

	wantResults := []struct{ test, name string }{
		{"users-table", "first"}, {"users-table", "second"}, {"login-flow", "signs in"},
	}
	if len(run.Results) != len(wantResults) {
		t.Fatalf("%d results; want %d", len(run.Results), len(wantResults))
	}
	for i, w := range wantResults {
		r := run.Results[i]
		if r.TestID != w.test || r.Name != w.name || r.Feature != "accounts" || r.Status != core.StatusPass {
			t.Errorf("result %d = %+v; want test %q, name %q, feature accounts, pass", i, r, w.test, w.name)
		}
	}

	// And it is really in the history file.
	stored, err := f.store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Results) != 3 || stored.Results[2].TestID != "login-flow" || stored.Environment != "local" {
		t.Errorf("stored run differs: %+v", stored)
	}
}

func TestSelection(t *testing.T) {
	cases := []struct {
		name string
		env  string
		sel  Selection
		want []string // ids of the tests that ran, in order
	}{
		{"everything", "local", Selection{}, []string{"users-table", "login-flow", "note-create"}},
		{"by ids, in manifest order", "local", Selection{TestIDs: []string{"note-create", "users-table"}}, []string{"users-table", "note-create"}},
		{"by feature", "local", Selection{Feature: "notes"}, []string{"note-create"}},
		{"ids and feature together", "local", Selection{TestIDs: []string{"login-flow", "users-table"}, Feature: "accounts"}, []string{"users-table", "login-flow"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.mustRun(c.env, c.sel)
			if got := startedIDs(f.events); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ran %v; want %v", got, c.want)
			}
		})
	}

	bad := []struct {
		name string
		env  string
		sel  Selection
		want string
	}{
		{"unknown id", "local", Selection{TestIDs: []string{"nope"}}, `test "nope" is not in the manifest`},
		{"id from another feature", "local", Selection{TestIDs: []string{"note-create"}, Feature: "accounts"}, `belongs to feature "notes", not "accounts"`},
		{"unknown feature", "local", Selection{Feature: "billing"}, `feature "billing" is not defined`},
		{"unknown environment", "nowhere", Selection{}, `environment "nowhere" is not defined`},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.run(c.env, c.sel)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v; want it to contain %q", err, c.want)
			}
			if len(f.runner.calls) != 0 || f.storedRuns() != 0 {
				t.Error("something ran or was saved although the request was wrong")
			}
		})
	}
}

func TestTestsThatCannotRunInTheEnvironment(t *testing.T) {
	f := newFixture(t)

	// "deployed" is not served by the sql runner: that test is skipped and
	// flagged, and the rest still runs.
	rep := f.mustRun("deployed", Selection{Feature: "accounts"})
	if len(rep.Skipped) != 1 || rep.Skipped[0].Test.ID != "users-table" {
		t.Fatalf("skipped %+v; want users-table", rep.Skipped)
	}
	if want := `its runner "sql" runs only in: local, not in deployed`; rep.Skipped[0].Reason != want {
		t.Errorf("reason %q; want %q", rep.Skipped[0].Reason, want)
	}
	if got := startedIDs(f.events); !reflect.DeepEqual(got, []string{"login-flow"}) {
		t.Errorf("ran %v; want only login-flow", got)
	}
	if f.events[0].Kind != EventSkipped || f.events[0].Test.ID != "users-table" || f.events[0].Reason != rep.Skipped[0].Reason {
		t.Errorf("first event %+v; want the skip, announced before anything runs", f.events[0])
	}
	if len(rep.Run.Results) != 1 {
		t.Errorf("%d results; a skipped test must not be saved as a result", len(rep.Run.Results))
	}

	// Asking for such a test by id is an error before anything runs.
	f = newFixture(t)
	_, err := f.run("deployed", Selection{TestIDs: []string{"users-table"}})
	if err == nil || !strings.Contains(err.Error(), `cannot run in environment "deployed"`) {
		t.Errorf("error = %v; want one saying the test cannot run there", err)
	}
	if len(f.runner.calls) != 0 || f.storedRuns() != 0 {
		t.Error("something ran or was saved")
	}

	// If every selected test would be skipped, that is an error listing why.
	f = newFixture(t)
	_, err = f.run("other", Selection{})
	if err == nil || !strings.Contains(err.Error(), "nothing to run") || !strings.Contains(err.Error(), `skipped test "login-flow"`) {
		t.Errorf("error = %v; want 'nothing to run' with the reasons", err)
	}
}

func TestPreviewChecksWithoutRunningAnything(t *testing.T) {
	f := newFixture(t)
	f.svc.Store = nil // a preview needs no store

	p, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "deployed", Select: Selection{Feature: "accounts"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tests) != 1 || p.Tests[0].ID != "login-flow" || len(p.Skipped) != 1 || p.Skipped[0].Test.ID != "users-table" {
		t.Errorf("preview = %+v; want login-flow to run and users-table skipped", p)
	}
	if len(f.runner.calls) != 0 {
		t.Errorf("%d commands ran during a preview", len(f.runner.calls))
	}

	// The same checks as a run: a wrong request is an error.
	for name, req := range map[string]Request{
		"an unknown environment": {Manifest: f.manifest, Environment: "nowhere"},
		"an unknown test":        {Manifest: f.manifest, Environment: "local", Select: Selection{TestIDs: []string{"nope"}}},
	} {
		if _, err := f.svc.Preview(bg, req); err == nil {
			t.Errorf("%s: the preview accepted it", name)
		}
	}
	f.svc.Creds = fakeCreds{}
	if _, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Environment: "deployed"}); err == nil || !strings.Contains(err.Error(), "ACCOUNT_PASSWORD") {
		t.Errorf("a preview with a missing secret: %v; want an error naming it", err)
	}
}

func TestTheEnvironmentACommandGets(t *testing.T) {
	f := newFixture(t)
	f.mustRun("deployed", Selection{TestIDs: []string{"login-flow"}})
	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/x",
		"BASE_URL=https://dev.example.test", // the environment's own value replaced the tester's
		"ACCOUNT_PASSWORD=pw-12345-abc",     // this environment's secret, from the credentials source
	}
	if got := f.runner.calls[0].Env; !reflect.DeepEqual(got, want) {
		t.Errorf("deployed environment:\n got  %v\n want %v\n(no other environment's secret, no pull token, no malformed entry)", got, want)
	}

	f = newFixture(t)
	f.mustRun("local", Selection{TestIDs: []string{"users-table"}})
	want = []string{"PATH=/usr/bin", "HOME=/home/x", "BASE_URL=http://127.0.0.1:8000"}
	if got := f.runner.calls[0].Env; !reflect.DeepEqual(got, want) {
		t.Errorf("local environment:\n got  %v\n want %v\n(the secret declared elsewhere must not leak in from the tester's own)", got, want)
	}
}

func TestSecretProblemsStopEverything(t *testing.T) {
	f := newFixture(t)
	f.svc.Creds = fakeCreds{}
	_, err := f.run("deployed", Selection{})
	if err == nil || !strings.Contains(err.Error(), "ACCOUNT_PASSWORD") || !strings.Contains(err.Error(), "not set") {
		t.Errorf("missing secret: %v; want an error naming it", err)
	}
	if len(f.runner.calls) != 0 || f.storedRuns() != 0 {
		t.Error("something ran or was saved with a secret missing")
	}

	f = newFixture(t)
	f.svc.Creds = fakeCreds{"ACCOUNT_PASSWORD": "abc"}
	_, err = f.run("deployed", Selection{})
	if err == nil || !strings.Contains(err.Error(), "ACCOUNT_PASSWORD") {
		t.Fatalf("short secret: %v; want an error naming it", err)
	}
	if strings.Contains(err.Error(), "abc") {
		t.Errorf("the error contains the secret's value: %v", err)
	}
	if len(f.runner.calls) != 0 {
		t.Error("a command ran with a secret too short to scrub")
	}
}

func TestSecretsNeverReachTheHistory(t *testing.T) {
	const pw = "S3cret!Pass-1"
	basic := base64.StdEncoding.EncodeToString([]byte("alice:" + pw))

	f := newFixture(t)
	f.svc.Creds = fakeCreds{"ACCOUNT_PASSWORD": pw}
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if n == 1 { // a failing result whose message leaks the secret
			return core.Output{
				Report: []byte(`<testsuite name="s"><testcase name="t"><failure message="rejected ` + pw + ` / Basic ` + basic + `"/></testcase></testsuite>`),
				Stderr: []byte("stderr: " + pw), ExitCode: 1,
			}, nil
		}
		// A crash whose output leaks it.
		return core.Output{Stdout: []byte("log: " + pw), Stderr: []byte("fatal: password " + pw + " rejected"), ExitCode: 2}, nil
	}
	rep := f.mustRun("deployed", Selection{TestIDs: []string{"login-flow", "note-create"}})

	stored, err := f.store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, r := range stored.Results {
		all.WriteString(r.Name + "\n" + r.Message + "\n")
	}
	all.WriteString(stored.Diagnostic)
	text := all.String()
	for _, secret := range []string{pw, basic} {
		if strings.Contains(text, secret) {
			t.Errorf("the saved run contains %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "[redacted]") {
		t.Errorf("nothing was redacted:\n%s", text)
	}
}

func TestFailuresThatTheResultsDoNotExplain(t *testing.T) {
	cases := []struct {
		name       string
		ids        []string
		reply      func(int, core.Invocation) (core.Output, error)
		wantCause  string // the error result's message
		wantExit   int
		wantBefore int // results that come before the error result
	}{
		{
			"a non-zero exit with every result passing", []string{"users-table"},
			func(int, core.Invocation) (core.Output, error) {
				return core.Output{Stdout: []byte("1..1\nok 1 - fine\n"), Stderr: []byte("boom: disk full\n"), ExitCode: 3}, nil
			},
			"exited with code 3: boom: disk full", 3, 1,
		},
		{
			"a non-zero exit with no results", []string{"users-table"},
			func(int, core.Invocation) (core.Output, error) {
				return core.Output{Stdout: []byte("Segmentation fault\n"), ExitCode: 2}, nil
			},
			"exited with code 2: Segmentation fault", 2, 0,
		},
		{
			"a timeout", []string{"users-table"},
			func(int, core.Invocation) (core.Output, error) {
				return core.Output{Stdout: []byte("1..1\nok 1 - a\n"), TimedOut: true, ExitCode: 143}, nil
			},
			"timed out after 10m0s: ok 1 - a", 143, 1,
		},
		{
			// pgTAP prints its plan first, so a query that fails leaves a plan
			// with no results. The parser's "planned 1 tests but saw 0" is not
			// kept beside the real cause: one error per crashed test.
			"a crash right after the plan line", []string{"users-table"},
			func(int, core.Invocation) (core.Output, error) {
				return core.Output{Stdout: []byte("1..1\n"), Stderr: []byte("Error: no such table: pipetting_log\n"), ExitCode: 1}, nil
			},
			"exited with code 1: Error: no such table: pipetting_log", 1, 0,
		},
		{
			"a command that cannot start", []string{"users-table"},
			func(int, core.Invocation) (core.Output, error) { return core.Output{}, errors.New("exec: not found") },
			"could not start the command: exec: not found", -1, 0,
		},
		{
			"an exit of 0 with no results", []string{"users-table"},
			func(int, core.Invocation) (core.Output, error) { return core.Output{}, nil },
			"no results", -1, 0,
		},
		{
			"results that cannot be read", []string{"login-flow"},
			func(int, core.Invocation) (core.Output, error) { return core.Output{Report: []byte("<html/>")}, nil },
			"could not read the results: junit: unexpected root element <html>", -1, 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.runner.reply = c.reply
			rep := f.mustRun("local", Selection{TestIDs: c.ids})
			run := rep.Run

			if len(run.Results) != c.wantBefore+1 {
				t.Fatalf("%d results %+v; want %d plus the error result", len(run.Results), run.Results, c.wantBefore)
			}
			last := run.Results[len(run.Results)-1]
			test, _ := f.manifest.Test(c.ids[0])
			if last.Status != core.StatusError || last.TestID != test.ID || last.Feature != test.Feature || last.Name != test.Name {
				t.Errorf("last result %+v; want an error result named for the test %q", last, test.ID)
			}
			if !strings.HasPrefix(last.Message, c.wantCause) {
				t.Errorf("message %q; want it to start with %q", last.Message, c.wantCause)
			}
			if run.ExitCode != c.wantExit {
				t.Errorf("run exit code %d; want %d", run.ExitCode, c.wantExit)
			}
			// A diagnostic exists only when the command printed more than the one
			// line already on the error row; when there is one, it names the test.
			if run.Diagnostic != "" && !strings.Contains(run.Diagnostic, "["+test.ID+"]") {
				t.Errorf("diagnostic %q does not name the test", run.Diagnostic)
			}
			v := run.Verdict()
			if v.Outcome != core.OutcomeFail {
				t.Errorf("verdict %+v; want fail", v)
			}
			// The reason counts failures; the cause of each is in its own result.
			if want := fmt.Sprintf("1 of %d results failed", c.wantBefore+1); v.Reason != want {
				t.Errorf("reason %q; want %q", v.Reason, want)
			}
		})
	}
}

// The diagnostic has one block per failed test. A block names the test and the
// cause, and shows no TAP plan line and no repeat of the cause's last line.
func TestDiagnosticHasOneBlockPerFailedTest(t *testing.T) {
	f := newFixture(t)
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		switch n {
		case 1:
			return core.Output{Stdout: []byte("1..1\n"), Stderr: []byte("context one\ncontext two\nError: no such table: log\n"), ExitCode: 2}, nil
		case 2:
			return core.Output{Stdout: []byte("# warming up\n# still going\n"), TimedOut: true, ExitCode: 143}, nil
		}
		return defaultReply(n, inv)
	}
	run := f.mustRun("local", Selection{TestIDs: []string{"users-table", "login-flow"}}).Run

	blocks := strings.Split(run.Diagnostic, "\n\n")
	if len(blocks) != 2 {
		t.Fatalf("%d blocks in %q; want one per failed test", len(blocks), run.Diagnostic)
	}
	if !strings.HasPrefix(blocks[0], "[users-table] exited with code 2: Error: no such table: log") ||
		!strings.HasPrefix(blocks[1], "[login-flow] timed out after") {
		t.Errorf("blocks %q do not start with the test and its cause", blocks)
	}
	if !strings.Contains(blocks[0], "context one\ncontext two") || !strings.Contains(blocks[1], "# warming up") {
		t.Errorf("blocks %q lack the output that came before each cause", blocks)
	}
	if strings.Contains(run.Diagnostic, "1..1") {
		t.Errorf("the diagnostic shows a TAP plan line: %q", run.Diagnostic)
	}
	if n := strings.Count(run.Diagnostic, "no such table"); n != 1 {
		t.Errorf("the cause appears %d times; want once: %q", n, run.Diagnostic)
	}
}

// When a command's whole output is the one line already on the error row, the
// diagnostic would only say it twice, so the run has none.
func TestNoDiagnosticWhenItWouldOnlyRepeatTheRow(t *testing.T) {
	f := newFixture(t)
	f.runner.reply = func(int, core.Invocation) (core.Output, error) {
		return core.Output{Stdout: []byte("1..1\n"), Stderr: []byte("kaboom\n"), ExitCode: 2}, nil
	}
	run := f.mustRun("local", Selection{TestIDs: []string{"users-table"}}).Run
	if run.Diagnostic != "" {
		t.Errorf("diagnostic %q; want none", run.Diagnostic)
	}
	if len(run.Results) != 1 || !strings.Contains(run.Results[0].Message, "kaboom") {
		t.Errorf("results %+v; want the one error row carrying the cause", run.Results)
	}
}

// What a test tool says beyond a failure's short message (a stack trace, say)
// goes to the diagnostic, headed by the test and the result; the result's row
// keeps only the short message.
func TestAToolsLongDetailGoesToTheDiagnosticNotTheRow(t *testing.T) {
	f := newFixture(t)
	f.runner.reply = func(int, core.Invocation) (core.Output, error) {
		report := "<testsuites><testcase name=\"rejects it\"><failure message=\"expected 401\">expected 401\n  at login.js:10</failure></testcase></testsuites>"
		return core.Output{Report: []byte(report)}, nil
	}
	run := f.mustRun("local", Selection{TestIDs: []string{"login-flow"}}).Run

	if len(run.Results) != 1 || run.Results[0].Message != "expected 401" {
		t.Fatalf("results %+v; want one failed result with only the short message", run.Results)
	}
	if want := "[login-flow] rejects it\nat login.js:10"; run.Diagnostic != want {
		t.Errorf("diagnostic %q; want %q", run.Diagnostic, want)
	}
}

func TestAFailureTheResultsDoExplainAddsNothing(t *testing.T) {
	f := newFixture(t)
	f.runner.reply = func(int, core.Invocation) (core.Output, error) {
		return core.Output{Stdout: []byte("1..2\nok 1 - a\nnot ok 2 - b\n# Failed\n"), ExitCode: 1}, nil
	}
	run := f.mustRun("local", Selection{TestIDs: []string{"users-table"}}).Run

	if len(run.Results) != 2 || run.Results[1].Status != core.StatusFail {
		t.Fatalf("results %+v; want exactly a pass and a fail", run.Results)
	}
	if run.ExitCode != 0 || run.Diagnostic != "" {
		t.Errorf("exit code %d, diagnostic %q; a non-zero exit that a failing result explains adds nothing", run.ExitCode, run.Diagnostic)
	}
	if v := run.Verdict(); v.Outcome != core.OutcomeFail || v.Reason != "1 of 2 results failed" {
		t.Errorf("verdict %+v", v)
	}
}

func TestACrashDoesNotStopTheOtherTests(t *testing.T) {
	f := newFixture(t)
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if n == 1 {
			return core.Output{Stderr: []byte("kaboom\n"), ExitCode: 2}, nil
		}
		return defaultReply(n, inv)
	}
	rep := f.mustRun("local", Selection{TestIDs: []string{"users-table", "login-flow"}})

	if len(f.runner.calls) != 2 {
		t.Fatalf("%d commands; want both tests to run", len(f.runner.calls))
	}
	run := rep.Run
	if len(run.Results) != 2 || run.Results[0].Status != core.StatusError || run.Results[1].Status != core.StatusPass {
		t.Fatalf("results %+v; want the crash, then the passing test", run.Results)
	}
	if v := run.Verdict(); v.Outcome != core.OutcomeFail || v.Reason != "1 of 2 results failed" || run.ExitCode != 2 {
		t.Errorf("verdict %+v, exit code %d; want a failed run that counts the crash as a failure", v, run.ExitCode)
	}
}

func TestCancellationDiscardsTheRun(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if n == 2 { // Ctrl-C during the second test
			cancel()
			return core.Output{Stdout: []byte("1..1\nok 1 - partial\n")}, fmt.Errorf("runner: stopped: %w", context.Canceled)
		}
		return defaultReply(n, inv)
	}
	rep, err := f.runWith(ctx, "local", Selection{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want one wrapping context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "after 1 of 3 tests") || !strings.Contains(err.Error(), "nothing was saved") {
		t.Errorf("error %q should say how far it got and that nothing was saved", err)
	}
	if rep.Saved || rep.RunID != 0 {
		t.Errorf("Saved %v, RunID %d; a cancelled run is not saved", rep.Saved, rep.RunID)
	}
	if len(rep.Run.Results) != 2 {
		t.Errorf("%d results returned for display; want the first test's 2", len(rep.Run.Results))
	}
	if len(f.runner.calls) != 2 {
		t.Errorf("%d commands; the third test must not start", len(f.runner.calls))
	}
	if f.storedRuns() != 0 {
		t.Error("a cancelled run was saved")
	}

	// A context that is already cancelled runs nothing.
	f = newFixture(t)
	done, stop := context.WithCancel(bg)
	stop()
	if _, err := f.runWith(done, "local", Selection{}); !errors.Is(err, context.Canceled) || len(f.runner.calls) != 0 {
		t.Errorf("an already-cancelled context: %v, %d commands", err, len(f.runner.calls))
	}
}

func TestAFailingStoreStillReturnsTheResults(t *testing.T) {
	f := newFixture(t)
	f.svc.Store = failingStore{f.store}
	rep, err := f.run("local", Selection{TestIDs: []string{"users-table"}})
	if err == nil || !strings.Contains(err.Error(), "saving the run") || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("error = %v; want the save failure", err)
	}
	if rep.Saved || rep.RunID != 0 || len(rep.Run.Results) != 2 {
		t.Errorf("report %+v; want the results handed back, unsaved", rep)
	}
}

func TestProgressEvents(t *testing.T) {
	f := newFixture(t)
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if n == 2 {
			return core.Output{Report: []byte(`<testsuite name="s"><testcase name="t"><failure message="no"/></testcase></testsuite>`)}, nil
		}
		return defaultReply(n, inv)
	}
	f.mustRun("local", Selection{Feature: "accounts"})

	type ev struct {
		kind         EventKind
		id           string
		index, total int
		failed       bool
	}
	var got []ev
	for _, e := range f.events {
		got = append(got, ev{e.Kind, e.Test.ID, e.Index, e.Total, e.Failed})
	}
	want := []ev{
		{EventStarted, "users-table", 1, 2, false}, {EventFinished, "users-table", 1, 2, false},
		{EventStarted, "login-flow", 2, 2, false}, {EventFinished, "login-flow", 2, 2, true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events:\n got  %+v\n want %+v", got, want)
	}
	if last := f.events[len(f.events)-1]; last.Reason != "1 of 1 results failed" {
		t.Errorf("finished reason %q", last.Reason)
	}
}

func TestTheFeatureIsRecordedWhenSaved(t *testing.T) {
	f := newFixture(t)
	rep := f.mustRun("local", Selection{TestIDs: []string{"users-table"}})

	// The test moves to another feature afterwards. History must not change.
	f.manifest.Tests[0].Feature = "notes"

	stored, err := f.store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stored.Results {
		if r.Feature != "accounts" {
			t.Errorf("result %q has feature %q; want the one it had when saved", r.Name, r.Feature)
		}
	}
}

func TestRunWithoutAManifest(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Run(bg, Request{Environment: "local"}); err == nil {
		t.Error("a request with no manifest was accepted")
	}
}
