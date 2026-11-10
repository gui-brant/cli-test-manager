package core

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseStatus(t *testing.T) {
	for _, want := range []Status{StatusPass, StatusFail, StatusSkip, StatusError} {
		got, err := ParseStatus(string(want))
		if err != nil || got != want {
			t.Errorf("ParseStatus(%q) = %q, %v; want %q, nil", want, got, err, want)
		}
		if got.String() != string(want) {
			t.Errorf("%q.String() = %q; want the same text back", want, got.String())
		}
	}
	// Matching is exact: near misses must be rejected, not guessed at.
	for _, bad := range []string{"", "PASS", "Pass", "passed", " pass", "pass ", "todo"} {
		if got, err := ParseStatus(bad); err == nil {
			t.Errorf("ParseStatus(%q) = %q, nil; want an error", bad, got)
		}
	}
}

func TestStatusFailed(t *testing.T) {
	cases := map[Status]bool{
		StatusPass:  false,
		StatusSkip:  false,
		StatusFail:  true,
		StatusError: true,
	}
	for st, want := range cases {
		if got := st.Failed(); got != want {
			t.Errorf("%q.Failed() = %v; want %v", st, got, want)
		}
	}
}

func TestParseRunState(t *testing.T) {
	for _, want := range []RunState{RunPending, RunComplete, RunExpired} {
		got, err := ParseRunState(string(want))
		if err != nil || got != want {
			t.Errorf("ParseRunState(%q) = %q, %v; want %q, nil", want, got, err, want)
		}
	}
	for _, bad := range []string{"", "Complete", "done", "in progress"} {
		if _, err := ParseRunState(bad); err == nil {
			t.Errorf("ParseRunState(%q) returned no error", bad)
		}
	}
}

func TestRunKey(t *testing.T) {
	first := Run{ExternalID: "9001", Attempt: 1}
	rerun := Run{ExternalID: "9001", Attempt: 2}

	k1, ok1 := first.Key()
	k2, ok2 := rerun.Key()
	if !ok1 || !ok2 {
		t.Fatalf("remote runs must have a key; got ok=%v, %v", ok1, ok2)
	}
	if k1 == k2 {
		t.Errorf("attempts 1 and 2 of the same run share the key %q; a re-run must not overwrite the first attempt", k1)
	}
	again, _ := Run{ExternalID: "9001", Attempt: 1}.Key()
	if again != k1 {
		t.Errorf("the same id and attempt gave %q then %q; the key must be stable", k1, again)
	}
	if k, ok := (Run{}).Key(); ok || k != "" {
		t.Errorf("a local run returned key %q, %v; want no key", k, ok)
	}
}

func TestRunFeatures(t *testing.T) {
	run := Run{Results: []Result{
		{TestID: "a", Feature: "notes"},
		{TestID: "b", Feature: "accounts"},
		{TestID: "c", Feature: "notes"},
		{Name: "unlisted result"}, // no test id, no feature
		{TestID: "d", Feature: GeneralFeature},
	}}
	want := []string{"accounts", "general", "notes"}
	if got := run.Features(); !reflect.DeepEqual(got, want) {
		t.Errorf("Features() = %v; want %v (distinct, sorted, unlisted not counted)", got, want)
	}
	if got := (Run{}).Features(); len(got) != 0 {
		t.Errorf("an empty run covers %v; want no features", got)
	}
}

func TestRunVerdict(t *testing.T) {
	pass := Result{Status: StatusPass}
	fail := Result{Status: StatusFail}
	errored := Result{Status: StatusError}
	skip := Result{Status: StatusSkip}

	cases := []struct {
		name        string
		run         Run
		want        Outcome
		reasonHas   string // substring expected in the reason, if any
		reasonEmpty bool
	}{
		{"all pass", Run{State: RunComplete, Results: []Result{pass, pass}}, OutcomePass, "", true},
		{"one fail", Run{State: RunComplete, Results: []Result{pass, fail}}, OutcomeFail, "1 of 2", false},
		{"an error counts as a failure", Run{State: RunComplete, Results: []Result{errored}}, OutcomeFail, "1 of 1", false},
		{"skips alone pass", Run{State: RunComplete, Results: []Result{skip, skip}}, OutcomePass, "", true},
		{"non-zero exit fails even when every result passed", Run{State: RunComplete, ExitCode: 3, Results: []Result{pass}}, OutcomeFail, "exited with code 3", false},
		{"no results fails", Run{State: RunComplete}, OutcomeFail, "no results", false},
		{"pending has no verdict", Run{State: RunPending}, OutcomeUndetermined, "in progress", false},
		{"expired has no verdict", Run{State: RunExpired}, OutcomeUndetermined, "expired", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.run.Verdict()
			if v.Outcome != c.want {
				t.Errorf("Outcome = %q; want %q (reason %q)", v.Outcome, c.want, v.Reason)
			}
			if c.reasonEmpty && v.Reason != "" {
				t.Errorf("Reason = %q; want empty for a pass", v.Reason)
			}
			if c.reasonHas != "" && !strings.Contains(v.Reason, c.reasonHas) {
				t.Errorf("Reason = %q; want it to contain %q", v.Reason, c.reasonHas)
			}
		})
	}
}

func TestVerdictShowsTheRealCause(t *testing.T) {
	cases := []struct {
		name string
		run  Run
		want string
	}{
		{
			"no results, cause known",
			Run{State: RunComplete, Diagnostic: "tool: could not resolve host\nerror: connection refused\n"},
			"error: connection refused",
		},
		{
			"non-zero exit with results, cause known",
			Run{State: RunComplete, ExitCode: 2, Results: []Result{{Status: StatusPass}}, Diagnostic: "panic: boom"},
			"panic: boom",
		},
		{
			"no diagnostic falls back to the generic reason",
			Run{State: RunComplete},
			"no results",
		},
		{
			"a blank diagnostic falls back too",
			Run{State: RunComplete, Diagnostic: "  \n\n"},
			"no results",
		},
		{
			"failed tests keep their own reason",
			Run{State: RunComplete, Results: []Result{{Status: StatusFail}}, Diagnostic: "noise"},
			"1 of 1 results failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.run.Verdict()
			if v.Outcome != OutcomeFail {
				t.Errorf("Outcome = %q; want %q", v.Outcome, OutcomeFail)
			}
			if v.Reason != c.want {
				t.Errorf("Reason = %q; want %q", v.Reason, c.want)
			}
		})
	}
}

func TestTailDiagnostic(t *testing.T) {
	short := "error: nothing to see\n"
	if got := TailDiagnostic(short); got != short {
		t.Errorf("short text changed: %q", got)
	}

	exact := strings.Repeat("x", MaxDiagnosticBytes)
	if got := TailDiagnostic(exact); got != exact {
		t.Error("text of exactly the limit must be returned unchanged")
	}

	long := strings.Repeat("x", MaxDiagnosticBytes) + "THE END"
	got := TailDiagnostic(long)
	if !strings.HasSuffix(got, "THE END") {
		t.Error("the end of the output must be kept: that is where the error is")
	}
	if !strings.HasPrefix(got, diagnosticCutMarker) {
		t.Error("a cut must be marked, so a reader knows the start is missing")
	}
	if len(got) > MaxDiagnosticBytes+len(diagnosticCutMarker) {
		t.Errorf("result is %d bytes; want at most %d plus the marker", len(got), MaxDiagnosticBytes)
	}

	// 8192 is not a multiple of 3, so cutting a run of 3-byte characters at a
	// fixed byte offset lands inside one. The result must still be valid text.
	multi := strings.Repeat("日", MaxDiagnosticBytes)
	if g := TailDiagnostic(multi); !utf8.ValidString(g) {
		t.Error("cutting must not split a multi-byte character")
	}
}

func TestResultListed(t *testing.T) {
	if (Result{TestID: "login-flow"}).Listed() != true {
		t.Error("a result with a test id must be listed")
	}
	if (Result{Name: "something printed by a runner"}).Listed() != false {
		t.Error("a result with no test id must be unlisted")
	}
}

func TestManifestHelpers(t *testing.T) {
	m := &Manifest{
		Features: []string{"accounts", "notes"},
		Tests: []TestSpec{
			{ID: "users-table", Feature: "accounts"},
			{ID: "login-flow", Feature: "accounts"},
		},
	}
	if got, ok := m.Test("login-flow"); !ok || got.ID != "login-flow" {
		t.Errorf("Test(login-flow) = %+v, %v; want the entry", got, ok)
	}
	if _, ok := m.Test("missing"); ok {
		t.Error("Test(missing) found something; want not found")
	}
	for name, want := range map[string]bool{
		"accounts":     true,
		"notes":        true,
		GeneralFeature: true, // always exists, even though it is not listed
		"billing":      false,
		"":             false,
	} {
		if got := m.HasFeature(name); got != want {
			t.Errorf("HasFeature(%q) = %v; want %v", name, got, want)
		}
	}
}

// Compile-time proof that each interface can be satisfied, and that its shape
// is usable for the tiny fakes later steps will write. If a method signature in
// ports.go changes in a way that breaks implementers, this file stops building.
type (
	fakeStore  struct{}
	fakeCreds  struct{}
	fakeSource struct{}
	fakeRunner struct{}
)

func (fakeStore) SaveRun(context.Context, Run) (int64, error)                { return 0, nil }
func (fakeStore) GetRun(context.Context, int64) (Run, error)                 { return Run{}, ErrNotFound }
func (fakeStore) FindByKey(context.Context, string, int) (Run, error)        { return Run{}, ErrNotFound }
func (fakeStore) ListRuns(context.Context, RunFilter) ([]RunSummary, error)  { return nil, nil }
func (fakeStore) TestStats(context.Context, StatsFilter) ([]TestStat, error) { return nil, nil }
func (fakeStore) Close() error                                               { return nil }

func (fakeCreds) Get(context.Context, string) (string, error) { return "", ErrNotFound }

func (fakeSource) ListRuns(context.Context, PullSpec, string) ([]RemoteRun, string, error) {
	return nil, "", nil
}
func (fakeSource) FetchArtifact(context.Context, PullSpec, RemoteRun) (io.ReadCloser, error) {
	return nil, ErrExpired
}

func (fakeRunner) Run(context.Context, Invocation) (Output, error) { return Output{}, nil }

var (
	_ Store        = fakeStore{}
	_ Credentials  = fakeCreds{}
	_ ResultSource = fakeSource{}
	_ Runner       = fakeRunner{}
)
