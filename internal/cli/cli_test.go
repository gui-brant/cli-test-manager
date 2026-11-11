package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// ---- fakes -----------------------------------------------------------------

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

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time {
	t := c.now
	c.now = c.now.Add(time.Second)
	return t
}

func defaultReply(_ int, inv core.Invocation) (core.Output, error) {
	switch inv.Args[0] {
	case "run-sql-tests":
		return core.Output{Stdout: []byte("1..2\nok 1 - first\nok 2 - second\n")}, nil
	case "http-runner":
		return core.Output{Report: []byte(`<testsuite name="s"><testcase name="signs in"/></testsuite>`)}, nil
	}
	return core.Output{}, errors.New("unexpected command " + inv.Args[0])
}

const failingHTTP = `<testsuite name="s"><testcase name="rejects a bad password"><failure message="expected 401, got 200"/></testcase></testsuite>`

// ---- the project under test ------------------------------------------------

const twoEnvironments = `
manifest_version: 1
project: example
history: .tester/history.sqlite
features: [accounts, notes]
environments:
  local:
    vars: { BASE_URL: "http://127.0.0.1:8000" }
  deployed:
    vars: { BASE_URL: "https://dev.example.test" }
    secrets: [ACCOUNT_PASSWORD]
runners:
  sql:
    run: ["run-sql-tests", "{files}"]
    format: tap
    output: stdout
    environments: [local]
    timeout: 10m
  http:
    run: ["http-runner", "--report", "{report}", "{files}"]
    format: junit
    output: file
    environments: [local, deployed]
    timeout: 10m
tests:
  - { id: users-table, name: Users table checks, feature: accounts, runner: sql,  file: tests/sql/users_table.sql }
  - { id: login-flow,  name: Login flow,         feature: accounts, runner: http, file: tests/http/login_flow.http }
  - { id: note-create, name: Create a note,      feature: notes,    runner: http, file: tests/http/note_create.http }
`

const oneEnvironment = `
manifest_version: 1
project: example
history: .tester/history.sqlite
features: [accounts]
environments:
  local:
    vars: { BASE_URL: "http://127.0.0.1:8000" }
runners:
  sql:
    run: ["run-sql-tests", "{files}"]
    format: tap
    output: stdout
    environments: [local]
    timeout: 10m
tests:
  - { id: users-table, name: Users table checks, feature: accounts, runner: sql, file: tests/sql/users_table.sql }
`

type project struct {
	t      *testing.T
	root   string
	cwd    string
	out    bytes.Buffer
	errOut bytes.Buffer
	runner *fakeRunner
	deps   Deps
}

func newProject(t *testing.T, manifestYAML string) *project {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("test-manifest.yaml", manifestYAML)
	write("tests/sql/users_table.sql", "select 1;\n")
	write("tests/http/login_flow.http", "GET /login\n")
	write("tests/http/note_create.http", "POST /notes\n")

	p := &project{t: t, root: root, cwd: root, runner: &fakeRunner{reply: defaultReply}}
	clock := &fakeClock{now: t0}
	p.deps = Deps{
		Stdin:    strings.NewReader(""),
		Stdout:   &p.out,
		Stderr:   &p.errOut,
		Getwd:    func() (string, error) { return p.cwd, nil },
		Environ:  func() []string { return []string{"PATH=/usr/bin"} },
		Now:      clock.Now,
		Location: time.UTC,
		Runner:   p.runner,
		Creds:    fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc"},
	}
	return p
}

func (p *project) do(args ...string) int { return p.doCtx(context.Background(), args...) }

func (p *project) doCtx(ctx context.Context, args ...string) int {
	p.out.Reset()
	p.errOut.Reset()
	return Main(ctx, args, p.deps)
}

func (p *project) stdout() string { return squash(p.out.String()) }
func (p *project) stderr() string { return squash(p.errOut.String()) }

func (p *project) historyExists() bool {
	_, err := os.Stat(filepath.Join(p.root, ".tester"))
	return err == nil
}

var spaces = regexp.MustCompile(`[ \t]+`)

// squash collapses runs of spaces, so table layout does not matter to the
// tests, and trims each line.
func squash(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
	}
	return strings.Join(lines, "\n")
}

func contains(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s does not contain %q:\n%s", what, w, text)
		}
	}
}

// ---- run -------------------------------------------------------------------

func TestRunThatPasses(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("run", "--env", "local"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(),
		"[1/3] users-table ... ok", "[2/3] login-flow ... ok", "[3/3] note-create ... ok",
		"Run 1: PASSED", "environment local, 4 results, 0 failed", "Details: tester history 1")
	if p.stderr() != "" {
		t.Errorf("stderr: %s", p.stderr())
	}

	// The run is in the history.
	if code := p.do("history"); code != ExitOK {
		t.Fatalf("history exit %d", code)
	}
	contains(t, "history", p.stdout(), "ID WHEN SOURCE ENV RESULT FAILED FEATURES REASON",
		"1 2026-11-10 12:00:00 local local PASS 0/4 accounts,notes")
}

func TestRunThatFails(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		return core.Output{Report: []byte(failingHTTP)}, nil
	}
	code := p.do("run", "--env", "local", "--test", "login-flow")
	if code != ExitFailed {
		t.Fatalf("exit %d; want %d", code, ExitFailed)
	}
	contains(t, "stdout", p.stdout(),
		"[1/1] login-flow ... FAILED: 1 of 1 results failed",
		"Run 1: FAILED (1 of 1 results failed)",
		"FAIL #2 login-flow rejects a bad password: expected 401, got 200")
}

func TestRunThatCannotStart(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		creds fakeCreds
		want  string
	}{
		{"unknown test", []string{"run", "--env", "local", "--test", "nope"}, nil, `test "nope" is not in the manifest`},
		{"unknown environment", []string{"run", "--env", "nowhere"}, nil, `environment "nowhere" is not defined`},
		{"no environment named", []string{"run"}, nil, "--env is required: the manifest defines deployed, local"},
		{"missing secret", []string{"run", "--env", "deployed", "--test", "login-flow"}, fakeCreds{}, "secrets not set: ACCOUNT_PASSWORD"},
		{"test that cannot run there", []string{"run", "--env", "deployed", "--test", "users-table"}, nil, `cannot run in environment "deployed"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			if c.creds != nil {
				p.deps.Creds = c.creds
			}
			if code := p.do(c.args...); code != ExitError {
				t.Fatalf("exit %d; want %d", code, ExitError)
			}
			contains(t, "stderr", p.stderr(), c.want)
			if len(p.runner.calls) != 0 || strings.Contains(p.stdout(), "Run ") {
				t.Errorf("something ran: %d commands, stdout %q", len(p.runner.calls), p.stdout())
			}
			// Nothing was saved.
			p.do("history")
			contains(t, "history", p.stdout(), "No runs yet.")
		})
	}
}

func TestAManifestWithOneEnvironmentNeedsNoEnvFlag(t *testing.T) {
	p := newProject(t, oneEnvironment)
	if code := p.do("run"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(), "environment local")
}

func TestSkippedTestsAreFlagged(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("run", "--env", "deployed"); code != ExitOK {
		t.Fatalf("exit %d; stderr: %s", code, p.stderr())
	}
	contains(t, "stdout", p.stdout(),
		`skipped users-table (its runner "sql" runs only in: local, not in deployed)`,
		"1 test skipped (listed above)", "[1/2] login-flow ... ok", "Run 1: PASSED")
}

func TestConfirmationBeforeRunningEveryTest(t *testing.T) {
	prompt := `Run all 3 tests in environment "local"? [y/N]`

	// Declined: nothing runs.
	p := newProject(t, twoEnvironments)
	p.deps.IsTerminal = true
	p.deps.Stdin = strings.NewReader("n\n")
	if code := p.do("run", "--env", "local"); code != ExitError {
		t.Errorf("declined: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), prompt, "not confirmed; nothing was run")
	if len(p.runner.calls) != 0 {
		t.Error("tests ran although the prompt was declined")
	}

	// Anything but yes is no, including an empty answer.
	p.deps.Stdin = strings.NewReader("\n")
	if code := p.do("run", "--env", "local"); code != ExitError {
		t.Errorf("empty answer: exit %d; want %d", code, ExitError)
	}

	// Accepted.
	p.deps.Stdin = strings.NewReader("yes\n")
	if code := p.do("run", "--env", "local"); code != ExitOK || len(p.runner.calls) != 3 {
		t.Errorf("accepted: exit %d, %d commands; want 0 and 3", code, len(p.runner.calls))
	}

	// No prompt with --yes, with a selection, or when input is not a terminal.
	for name, args := range map[string][]string{
		"--yes":     {"run", "--env", "local", "--yes"},
		"--feature": {"run", "--env", "local", "--feature", "accounts"},
		"--test":    {"run", "--env", "local", "--test", "login-flow"},
	} {
		p := newProject(t, twoEnvironments)
		p.deps.IsTerminal = true
		p.deps.Stdin = strings.NewReader("") // nobody to answer
		if code := p.do(args...); code != ExitOK {
			t.Errorf("%s: exit %d; want it to run without asking", name, code)
		}
		if strings.Contains(p.stderr(), "[y/N]") {
			t.Errorf("%s: asked for confirmation", name)
		}
	}
	p = newProject(t, twoEnvironments) // a push hook: input is not a terminal
	if code := p.do("run", "--env", "local"); code != ExitOK || strings.Contains(p.stderr(), "[y/N]") {
		t.Errorf("not a terminal: exit %d, stderr %q; want a run with no prompt", code, p.stderr())
	}
}

// The bug the owner found by using the program: `run --env nowhere` asked "Are
// you sure?" about an environment that does not exist, and only complained
// after the answer. A run that cannot start must be refused BEFORE any question.
func TestNoQuestionIsAskedAboutARunThatCannotStart(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		creds fakeCreds
		want  string
	}{
		{"an unknown environment", []string{"run", "--env", "nowhere"}, nil, `environment "nowhere" is not defined`},
		{"another unknown environment", []string{"run", "--env", "asdfadfa"}, nil, `environment "asdfadfa" is not defined`},
		{"a missing secret", []string{"run", "--env", "deployed"}, fakeCreds{}, "secrets not set: ACCOUNT_PASSWORD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			p.deps.IsTerminal = true
			p.deps.Stdin = strings.NewReader("y\n") // would say yes, if it were asked
			if c.creds != nil {
				p.deps.Creds = c.creds
			}
			if code := p.do(c.args...); code != ExitError {
				t.Fatalf("exit %d; want %d", code, ExitError)
			}
			contains(t, "stderr", p.stderr(), c.want)
			if strings.Contains(p.stderr()+p.stdout(), "[y/N]") {
				t.Error("the question was asked about a run that could not start")
			}
			if len(p.runner.calls) != 0 {
				t.Error("a command ran")
			}
			if p.historyExists() {
				t.Error("a request that could not start created the history file")
			}
		})
	}
}

// The count in the question must match what will happen: tests that will be
// skipped are not counted as run.
func TestTheQuestionCountsWhatWillReallyRun(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.IsTerminal = true
	p.deps.Stdin = strings.NewReader("n\n")
	p.do("run", "--env", "deployed") // users-table cannot run there
	contains(t, "stderr", p.stderr(), `Run 2 of 3 tests in environment "deployed" (1 skipped)? [y/N]`)
	if len(p.runner.calls) != 0 {
		t.Error("tests ran although the question was declined")
	}
}

func TestCtrlCDiscardsTheRun(t *testing.T) {
	p := newProject(t, twoEnvironments)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if n == 2 {
			cancel()
			return core.Output{}, fmt.Errorf("runner: stopped: %w", context.Canceled)
		}
		return defaultReply(n, inv)
	}
	if code := p.doCtx(ctx, "run", "--env", "local"); code != ExitCancelled {
		t.Fatalf("exit %d; want %d", code, ExitCancelled)
	}
	contains(t, "stderr", p.stderr(), "nothing was saved")
	if strings.Contains(p.stdout(), "Run 1") {
		t.Errorf("a summary was printed for a cancelled run: %s", p.stdout())
	}
	p.do("history")
	contains(t, "history", p.stdout(), "No runs yet.")
}

func TestSecretsNeverReachTheTerminal(t *testing.T) {
	const pw = "S3cret!Pass-1"
	basic := base64.StdEncoding.EncodeToString([]byte("alice:" + pw))
	p := newProject(t, twoEnvironments)
	p.deps.Creds = fakeCreds{"ACCOUNT_PASSWORD": pw}
	p.runner.reply = func(int, core.Invocation) (core.Output, error) {
		xml := `<testsuite name="s"><testcase name="t"><failure message="rejected ` + pw + ` Basic ` + basic + `"/></testcase></testsuite>`
		return core.Output{Report: []byte(xml)}, nil
	}
	if code := p.do("run", "--env", "deployed", "--test", "login-flow"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	all := p.out.String() + p.errOut.String()
	p.do("history", "1")
	all += p.out.String()
	for _, secret := range []string{pw, basic} {
		if strings.Contains(all, secret) {
			t.Errorf("the output contains a secret %q", secret)
		}
	}
	contains(t, "output", all, "[redacted]")
}

// ---- history ---------------------------------------------------------------

func TestHistoryListAndDetail(t *testing.T) {
	p := newProject(t, twoEnvironments)
	// Run 1: everything passes. Run 2: one test fails.
	p.do("run", "--env", "local")
	p.runner.reply = func(int, core.Invocation) (core.Output, error) {
		return core.Output{Report: []byte(failingHTTP)}, nil
	}
	p.do("run", "--env", "local", "--test", "login-flow")

	p.do("history")
	out := "\n" + p.stdout()
	if i2, i1 := strings.Index(out, "\n2 "), strings.Index(out, "\n1 "); i2 < 0 || i1 < 0 || i2 > i1 {
		t.Errorf("runs are not newest first:\n%s", out)
	}
	contains(t, "history", out, "2 2026-11-10 12:00:02 local local FAIL 1/1 accounts 1 of 1 results failed")

	p.do("history", "--limit", "1")
	if strings.Contains(p.stdout(), "\n1 ") || !strings.Contains(p.stdout(), "2026-11-10 12:00:02") {
		t.Errorf("--limit 1 did not keep only the newest run:\n%s", p.stdout())
	}
	p.do("history", "--feature", "notes")
	if o := "\n" + p.stdout(); !strings.Contains(o, "\n1 ") || strings.Contains(o, "\n2 ") {
		t.Errorf("--feature notes should list only run 1:\n%s", p.stdout())
	}
	p.do("history", "--env", "deployed")
	contains(t, "history", p.stdout(), "No runs match.")
	p.do("history", "--source", "remote")
	contains(t, "history", p.stdout(), "No runs match.")
	if code := p.do("history", "--source", "bogus"); code != ExitError {
		t.Errorf("--source bogus: exit %d", code)
	}

	if code := p.do("history", "2"); code != ExitOK {
		t.Fatalf("history 2: exit %d; %s", code, p.stderr())
	}
	contains(t, "run detail", p.stdout(),
		"Run 2 FAIL", "reason 1 of 1 results failed", "source local, environment local",
		"Results:", "#2 FAIL login-flow rejects a bad password", "expected 401, got 200")

	if code := p.do("history", "99"); code != ExitError {
		t.Errorf("history 99: exit %d", code)
	}
	contains(t, "stderr", p.stderr(), "run 99 not found")
	if code := p.do("history", "abc"); code != ExitError {
		t.Errorf("history abc: exit %d", code)
	}
	contains(t, "stderr", p.stderr(), `"abc" is not a run number`)
}

func TestRunDetailShowsTheDiagnostic(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.runner.reply = func(int, core.Invocation) (core.Output, error) {
		return core.Output{Stderr: []byte("while loading the schema\nkaboom\n"), ExitCode: 2}, nil
	}
	if code := p.do("run", "--env", "local", "--test", "users-table"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	contains(t, "summary", p.stdout(), "Run 1: FAILED (1 of 1 results failed)", "ERROR #1 users-table Users table checks: exited with code 2: kaboom")

	p.do("history", "1")
	contains(t, "detail", p.stdout(), "exit code 2", "Diagnostic:", "[users-table] exited with code 2: kaboom", "while loading the schema")

	// A command whose whole output is the one line on the error row has no
	// Diagnostic section: it would only repeat the row.
	p.runner.reply = func(int, core.Invocation) (core.Output, error) {
		return core.Output{Stderr: []byte("kaboom\n"), ExitCode: 2}, nil
	}
	p.do("run", "--env", "local", "--test", "users-table")
	p.do("history", "2")
	if strings.Contains(p.stdout(), "Diagnostic:") {
		t.Errorf("a diagnostic that only repeats the row was shown:\n%s", p.stdout())
	}

	// The list does not pick one failure's cause: it counts, and points at the details.
	p.do("history")
	contains(t, "list", p.stdout(), "1 of 1 results failed; details: tester history 1")
}

// The test tool's own detail (stack traces, error objects) is long, so a run's
// view only says that it exists and how to see it; --detail shows it, before or
// after the run number.
func TestHistoryDetailIsHiddenUntilAskedFor(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.runner.reply = func(int, core.Invocation) (core.Output, error) {
		report := "<testsuite name=\"s\"><testcase name=\"rejects it\"><failure message=\"expected 401\">expected 401\n  at login.js:10</failure></testcase></testsuite>"
		return core.Output{Report: []byte(report)}, nil
	}
	p.do("run", "--env", "local", "--test", "login-flow")

	p.do("history", "1")
	contains(t, "default view", p.stdout(), "expected 401", "Show it with: tester history 1 --detail")
	if strings.Contains(p.stdout(), "login.js:10") || strings.Contains(p.stdout(), "Tool detail:") {
		t.Errorf("the tool's detail was shown without asking:\n%s", p.stdout())
	}

	for _, args := range [][]string{{"history", "1", "--detail"}, {"history", "--detail", "1"}} {
		if code := p.do(args...); code != ExitOK {
			t.Fatalf("%v: exit %d; %s", args, code, p.stderr())
		}
		contains(t, strings.Join(args, " "), p.stdout(), "Tool detail:", "[login-flow] rejects it", "at login.js:10")
		if strings.Contains(p.stdout(), "Show it with") {
			t.Errorf("%v: the hint was shown although the detail was asked for", args)
		}
	}

	if code := p.do("history", "--detail"); code != ExitError {
		t.Errorf("--detail without a run number: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "--detail shows one run in full")
	if code := p.do("history", "1", "2"); code != ExitError {
		t.Errorf("two run numbers: exit %d; want %d", code, ExitError)
	}
}

// Both kinds of help say what --detail is for, in plain words.
func TestHelpExplainsWhatDetailIsFor(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.do("help")
	contains(t, "help", p.stdout(),
		"history <run number> [--detail]",
		"--detail also show the test tool's own detail on the failed results",
		"stack traces, error objects",
		"hidden unless you ask",
		"Tool detail only with --detail")

	p.do("history", "-h")
	contains(t, "history -h", p.stderr(), "-detail", "stack traces, error objects", "hidden unless asked for")
}

func TestNoHistoryYet(t *testing.T) {
	p := newProject(t, twoEnvironments)
	for _, cmd := range [][]string{{"history"}, {"counts"}} {
		if code := p.do(cmd...); code != ExitOK {
			t.Errorf("%v: exit %d", cmd, code)
		}
		contains(t, strings.Join(cmd, " "), p.stdout(), "No runs yet.")
	}
	if code := p.do("history", "1"); code != ExitError {
		t.Errorf("history 1 with no history: exit %d", code)
	}
	contains(t, "stderr", p.stderr(), "there is no history yet")
	if p.historyExists() {
		t.Error("reading an empty history created the history file")
	}
}

// ---- tests, counts, check ---------------------------------------------------

func TestTestsList(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("tests"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	want := strings.Join([]string{
		"# ID NAME FEATURE RUNNER TAG FILE",
		"1 users-table Users table checks accounts sql - tests/sql/users_table.sql",
		"2 login-flow Login flow accounts http - tests/http/login_flow.http",
		"3 note-create Create a note notes http - tests/http/note_create.http",
	}, "\n")
	if got := strings.TrimSpace(p.stdout()); got != want {
		t.Errorf("tests list:\n%s\nwant:\n%s", got, want)
	}

	p.do("tests", "--feature", "notes")
	if o := p.stdout(); !strings.Contains(o, "3 note-create") || strings.Contains(o, "users-table") {
		t.Errorf("--feature notes should list only test 3:\n%s", o)
	}
	if code := p.do("tests", "--feature", "billing"); code != ExitError {
		t.Errorf("unknown feature: exit %d", code)
	}
	contains(t, "stderr", p.stderr(), `feature "billing" is not defined`)
}

func TestTestsShow(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("tests", "show", "login-flow"); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stderr())
	}
	contains(t, "show", p.stdout(), "#2 login-flow Login flow", "feature: accounts runner: http tag: -",
		"file: tests/http/login_flow.http", "GET /login")

	for name, args := range map[string][]string{
		"unknown id": {"tests", "show", "nope"},
		"no id":      {"tests", "show"},
	} {
		if code := p.do(args...); code != ExitError {
			t.Errorf("%s: exit %d; want %d", name, code, ExitError)
		}
	}

	if err := os.Remove(filepath.Join(p.root, "tests/http/login_flow.http")); err != nil {
		t.Fatal(err)
	}
	if code := p.do("tests", "show", "login-flow"); code != ExitError {
		t.Errorf("missing file: exit %d", code)
	}
	contains(t, "stderr", p.stderr(), "cannot read the test file tests/http/login_flow.http")
}

func TestCounts(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.do("run", "--env", "local")
	p.runner.reply = func(int, core.Invocation) (core.Output, error) {
		return core.Output{Report: []byte(failingHTTP)}, nil
	}
	p.do("run", "--env", "local", "--test", "login-flow")

	if code := p.do("counts"); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stderr())
	}
	out := p.stdout()
	contains(t, "counts", out, "# ID NAME RUNS PASS FAIL ERROR SKIP LAST LAST RUN",
		"1 users-table Users table checks 1 2 0 0 0 PASS", "2 login-flow Login flow 2 1 1 0 0 FAIL", "3 note-create Create a note 1 1 0 0 0 PASS")
	if i1, i2 := strings.Index(out, "1 users-table"), strings.Index(out, "2 login-flow"); i1 > i2 {
		t.Errorf("counts are not in manifest order:\n%s", out)
	}

	p.do("counts", "--feature", "notes")
	if o := p.stdout(); !strings.Contains(o, "note-create") || strings.Contains(o, "login-flow") {
		t.Errorf("--feature notes:\n%s", o)
	}
	p.do("counts", "--since", "2026-11-11")
	contains(t, "counts", p.stdout(), "No results match.")
	p.do("counts", "--since", "2026-11-10")
	contains(t, "counts", p.stdout(), "login-flow")
	if code := p.do("counts", "--since", "nonsense"); code != ExitError {
		t.Errorf("--since nonsense: exit %d", code)
	}
}

func TestCheck(t *testing.T) {
	p := newProject(t, twoEnvironments)
	if code := p.do("check"); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stdout())
	}
	contains(t, "check", p.stdout(), "Manifest OK: 3 tests, every test file exists.")

	// A missing test file is reported, together with any other.
	_ = os.Remove(filepath.Join(p.root, "tests/http/login_flow.http"))
	_ = os.Remove(filepath.Join(p.root, "tests/http/note_create.http"))
	if code := p.do("check"); code != ExitFailed {
		t.Errorf("missing files: exit %d; want %d", code, ExitFailed)
	}
	contains(t, "check", p.stdout(), "Test file problems (2):",
		`tests[1] (login-flow): file "tests/http/login_flow.http" does not exist`,
		`tests[2] (note-create): file "tests/http/note_create.http" does not exist`)

	// A manifest with a problem is reported before the files are looked at.
	p = newProject(t, strings.Replace(twoEnvironments, "runner: sql,", "runner: nope,", 1))
	if code := p.do("check"); code != ExitFailed {
		t.Errorf("invalid manifest: exit %d; want %d", code, ExitFailed)
	}
	contains(t, "check", p.stdout(), "manifest has 1 problem", `runner "nope" is not defined`, "checked once the manifest is valid")

	// A manifest that cannot be read at all is a different failure.
	p = newProject(t, twoEnvironments+"bogus: 1\n")
	if code := p.do("check"); code != ExitError {
		t.Errorf("unreadable manifest: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "bogus")
}

func TestOnlyRunCreatesTheHistoryFile(t *testing.T) {
	p := newProject(t, twoEnvironments)
	for _, cmd := range [][]string{{"tests"}, {"tests", "show", "login-flow"}, {"check"}, {"history"}, {"counts"}, {"help"}} {
		p.do(cmd...)
		if p.historyExists() {
			t.Fatalf("%v created the history file", cmd)
		}
	}
	p.do("run", "--env", "local")
	if !p.historyExists() {
		t.Error("run did not create the history file")
	}
}

// ---- finding the manifest, and usage ----------------------------------------

func TestFindingTheManifest(t *testing.T) {
	p := newProject(t, twoEnvironments)

	// From a folder inside the project.
	p.cwd = filepath.Join(p.root, "tests", "sql")
	if code := p.do("tests"); code != ExitOK {
		t.Errorf("from a nested folder: exit %d; %s", code, p.stderr())
	}

	// From anywhere, with --manifest.
	p.cwd = t.TempDir()
	if code := p.do("tests"); code != ExitError {
		t.Errorf("with no manifest in reach: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "test-manifest.yaml")
	if code := p.do("--manifest", filepath.Join(p.root, "test-manifest.yaml"), "tests"); code != ExitOK {
		t.Errorf("--manifest PATH: exit %d; %s", code, p.stderr())
	}
	if code := p.do("--manifest="+filepath.Join(p.root, "test-manifest.yaml"), "tests"); code != ExitOK {
		t.Errorf("--manifest=PATH: exit %d; %s", code, p.stderr())
	}
}

// --manifest takes the manifest file, or the project folder that holds it. A
// folder with no manifest in it is a clear error that names the file looked for.
func TestManifestOptionAcceptsAFolder(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.cwd = t.TempDir() // nowhere near the project

	if code := p.do("--manifest", p.root, "check"); code != ExitOK {
		t.Errorf("--manifest <folder>: exit %d; %s %s", code, p.stdout(), p.stderr())
	}
	if code := p.do("--manifest", filepath.Join(p.root, "test-manifest.yaml"), "check"); code != ExitOK {
		t.Errorf("--manifest <file>: exit %d", code)
	}

	empty := t.TempDir()
	if code := p.do("--manifest", empty, "check"); code != ExitError {
		t.Errorf("a folder with no manifest: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "test-manifest.yaml")
}

func TestUsageAndBadRequests(t *testing.T) {
	p := newProject(t, twoEnvironments)

	if code := p.do(); code != ExitError {
		t.Errorf("no arguments: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "Usage: tester")

	if code := p.do("help"); code != ExitOK {
		t.Errorf("help: exit %d", code)
	}
	contains(t, "help", p.stdout(), "Commands:", "Exit codes: 0 all passed, 1 a test failed")

	for name, args := range map[string][]string{
		"unknown command":      {"frobnicate"},
		"unknown flag":         {"run", "--bogus"},
		"extra argument":       {"run", "extra"},
		"a flag with no value": {"history", "--limit"},
		"a bad number":         {"history", "--limit", "x"},
		"--manifest, no path":  {"--manifest"},
	} {
		if code := p.do(args...); code != ExitError {
			t.Errorf("%s: exit %d; want %d", name, code, ExitError)
		}
	}
	p.do("frobnicate")
	contains(t, "stderr", p.stderr(), `unknown command "frobnicate"`)
	p.do("run", "extra")
	contains(t, "stderr", p.stderr(), `unexpected argument "extra"`)
}
