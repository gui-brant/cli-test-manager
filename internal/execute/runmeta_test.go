package execute

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// fakeCheckout answers with a fixed commit and dirty flag and notes the folders
// it was asked about.
type fakeCheckout struct {
	commit string
	dirty  bool
	dirs   []string
}

func (c *fakeCheckout) State(_ context.Context, dir string) (string, bool) {
	c.dirs = append(c.dirs, dir)
	return c.commit, c.dirty
}

// withVersions gives both runners a version command and makes the fake runner
// answer them from the map (keyed by the program name). Everything else gets
// the usual replies.
func withVersions(f *fixture, answers map[string]func() (core.Output, error)) {
	for name, cmd := range map[string][]string{
		"sql":  {"run-sql-tests", "--version"},
		"http": {"http-runner", "--version"},
	} {
		r := f.manifest.Runners[name]
		r.Version = cmd
		f.manifest.Runners[name] = r
	}
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if inv.Args[len(inv.Args)-1] == "--version" {
			if answer, ok := answers[inv.Args[0]]; ok {
				return answer()
			}
			return core.Output{Stdout: []byte(inv.Args[0] + " 1.0.0\n")}, nil
		}
		return defaultReply(n, inv)
	}
}

func out(stdout string) func() (core.Output, error) {
	return func() (core.Output, error) { return core.Output{Stdout: []byte(stdout)}, nil }
}

func storedRun(t *testing.T, f *fixture, rep Report) core.Run {
	t.Helper()
	stored, err := f.store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestARunRecordsTheCommitTheDirtyFlagAndTheTrigger(t *testing.T) {
	f := newFixture(t)
	co := &fakeCheckout{commit: "abc123def456", dirty: true}
	f.svc.Checkout = co

	rep, err := f.svc.Run(bg, Request{
		Manifest: f.manifest, Root: "/project", Environment: "local",
		Select: Selection{Feature: "accounts"}, Trigger: core.TriggerHook,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := storedRun(t, f, rep)
	if stored.CommitSHA != "abc123def456" || !stored.Dirty || stored.Trigger != core.TriggerHook {
		t.Errorf("stored commit %q, dirty %v, trigger %q; want abc123def456, true, hook", stored.CommitSHA, stored.Dirty, stored.Trigger)
	}
	if !reflect.DeepEqual(co.dirs, []string{"/project"}) {
		t.Errorf("the checkout was asked about %v; want the project folder once", co.dirs)
	}
}

func TestWithNoCheckoutAndNoTriggerARunIsManualAndHasNoCommit(t *testing.T) {
	f := newFixture(t) // no Checkout set
	stored := storedRun(t, f, f.mustRun("local", Selection{Feature: "accounts"}))
	if stored.CommitSHA != "" || stored.Dirty {
		t.Errorf("commit %q, dirty %v; want neither recorded", stored.CommitSHA, stored.Dirty)
	}
	if stored.Trigger != core.TriggerManual {
		t.Errorf("trigger %q; want manual", stored.Trigger)
	}
}

// The commit is read before any test runs, so a test that edits a file cannot
// change the answer. The version commands come first too.
func TestVersionsAndCommitAreReadBeforeTheFirstTest(t *testing.T) {
	f := newFixture(t)
	withVersions(f, nil)
	f.svc.Checkout = &fakeCheckout{commit: "abc"}
	f.mustRun("local", Selection{Feature: "accounts"})

	var order []string
	for _, c := range f.runner.calls {
		kind := "test"
		if c.Args[len(c.Args)-1] == "--version" {
			kind = "version"
		}
		order = append(order, kind)
	}
	if want := []string{"version", "version", "test", "test"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order of commands = %v; want %v", order, want)
	}
}

func TestToolVersionsAreRecordedInRunnerOrderOncePerRunner(t *testing.T) {
	f := newFixture(t)
	withVersions(f, map[string]func() (core.Output, error){
		"run-sql-tests": out("supabase 2.1.0\n"),
		"http-runner":   out("hurl 6.0.0\nbuilt with curl\n"),
	})
	// Two http tests (login-flow and note-create) but one http version command.
	rep := f.mustRun("local", Selection{TestIDs: []string{"users-table", "login-flow", "note-create"}})

	want := []core.Tool{{Name: "http", Version: "hurl 6.0.0"}, {Name: "sql", Version: "supabase 2.1.0"}}
	if got := storedRun(t, f, rep).Tools; !reflect.DeepEqual(got, want) {
		t.Errorf("tools = %+v; want %+v", got, want)
	}
	versionCalls := 0
	for _, c := range f.runner.calls {
		if c.Args[len(c.Args)-1] == "--version" {
			versionCalls++
		}
	}
	if versionCalls != 2 {
		t.Errorf("%d version commands started; want one per runner (2)", versionCalls)
	}
}

// A runner no selected test uses is not asked.
func TestOnlyTheRunnersOfTheSelectedTestsAreAsked(t *testing.T) {
	f := newFixture(t)
	withVersions(f, nil)
	rep := f.mustRun("local", Selection{Feature: "notes"}) // note-create: http only

	if got := storedRun(t, f, rep).Tools; len(got) != 1 || got[0].Name != "http" {
		t.Errorf("tools = %+v; want only http", got)
	}
	for _, c := range f.runner.calls {
		if c.Args[0] == "run-sql-tests" {
			t.Errorf("the sql runner was started: %v", c.Args)
		}
	}
}

func TestARunnerWithNoVersionCommandIsRecordedAsAGap(t *testing.T) {
	f := newFixture(t) // the fixture's runners declare none
	rep := f.mustRun("local", Selection{Feature: "accounts"})

	want := []core.Tool{
		{Name: "http", Version: "unknown: no version command declared"},
		{Name: "sql", Version: "unknown: no version command declared"},
	}
	if got := storedRun(t, f, rep).Tools; !reflect.DeepEqual(got, want) {
		t.Errorf("tools = %+v; want %+v", got, want)
	}
	if len(f.runner.calls) != 2 { // the two tests only
		t.Errorf("%d commands started; want only the 2 tests", len(f.runner.calls))
	}
}

// The version command is the manifest's list as written, in the project folder,
// with exactly the environment the tests get and a 10-second limit.
func TestTheVersionCommandRunsLikeATest(t *testing.T) {
	f := newFixture(t)
	withVersions(f, nil)
	f.mustRun("deployed", Selection{Feature: "notes"})

	var version, test *core.Invocation
	for i := range f.runner.calls {
		c := &f.runner.calls[i]
		if c.Args[len(c.Args)-1] == "--version" {
			version = c
		} else {
			test = c
		}
	}
	if version == nil || test == nil {
		t.Fatalf("calls = %+v", f.runner.calls)
	}
	if !reflect.DeepEqual(version.Args, []string{"http-runner", "--version"}) {
		t.Errorf("args = %v", version.Args)
	}
	if version.Dir != "/project" || version.Timeout != 10*time.Second || version.WantsReport {
		t.Errorf("dir %q, timeout %v, wants report %v", version.Dir, version.Timeout, version.WantsReport)
	}
	if !reflect.DeepEqual(version.Env, test.Env) {
		t.Errorf("the version command's environment differs from the test's:\n version %v\n test    %v", version.Env, test.Env)
	}
}

// Whatever goes wrong reading a version, it is recorded and the tests still
// run: a flaky --version must not block a push.
func TestAVersionThatCannotBeReadIsRecordedAndNeverFailsTheRun(t *testing.T) {
	cases := []struct {
		name   string
		answer func() (core.Output, error)
		want   string
	}{
		{"cannot start", func() (core.Output, error) { return core.Output{}, errors.New("executable file not found") },
			"unknown: could not run the command: executable file not found"},
		{"exits non-zero", func() (core.Output, error) { return core.Output{ExitCode: 3, Stdout: []byte("usage\n")}, nil },
			"unknown: the command exited with code 3"},
		{"times out", func() (core.Output, error) { return core.Output{TimedOut: true}, nil },
			"unknown: timed out after 10s"},
		{"prints nothing", out("  \n\n"), "unknown: the command printed nothing"},
		{"prints on standard error", func() (core.Output, error) { return core.Output{Stderr: []byte("openjdk 21.0.1\n")}, nil },
			"openjdk 21.0.1"},
		{"leading blank lines and a second line", out("\n\n  v1.2.3  \nsecond line\n"), "v1.2.3"},
		{"a very long line", out(strings.Repeat("x", 300) + "\n"), strings.Repeat("x", 200) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			withVersions(f, map[string]func() (core.Output, error){"http-runner": c.answer})
			rep := f.mustRun("local", Selection{Feature: "notes"})

			if rep.Run.Verdict().Outcome != core.OutcomePass {
				t.Errorf("the run is %v (%s); want it to pass", rep.Run.Verdict().Outcome, rep.Run.Verdict().Reason)
			}
			stored := storedRun(t, f, rep)
			if len(stored.Results) == 0 {
				t.Error("no test ran")
			}
			if len(stored.Tools) != 1 || stored.Tools[0].Version != c.want {
				t.Errorf("tools = %+v; want the version %q", stored.Tools, c.want)
			}
		})
	}
}

// A version command that prints a secret leaves no secret in the history.
func TestASecretInAVersionLineIsScrubbed(t *testing.T) {
	f := newFixture(t)
	withVersions(f, map[string]func() (core.Output, error){"http-runner": out("hurl 6.0 token pw-12345-abc\n")})
	rep := f.mustRun("deployed", Selection{Feature: "notes"})

	got := storedRun(t, f, rep).Tools[0].Version
	if strings.Contains(got, "pw-12345-abc") || got != "hurl 6.0 token [redacted]" {
		t.Errorf("version = %q; want the secret scrubbed", got)
	}
}

// A preview starts nothing, version commands included.
func TestAPreviewStartsNoVersionCommand(t *testing.T) {
	f := newFixture(t)
	withVersions(f, nil)
	if _, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runner.calls) != 0 {
		t.Errorf("a preview started %d commands", len(f.runner.calls))
	}
}
