package view

import (
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/query"
)

var (
	start  = time.Date(2026, 11, 11, 15, 9, 32, 0, time.UTC)
	finish = time.Date(2026, 11, 11, 15, 9, 38, 0, time.UTC)
	est    = time.FixedZone("EST", -5*3600)
)

func result(testID, name string, status core.Status, message string, number int) query.DetailResult {
	return query.DetailResult{
		Result: core.Result{TestID: testID, Name: name, Status: status, Message: message},
		Number: number,
	}
}

// What a run was made against: the short commit, the uncommitted-changes note,
// what started it and each tool's version, one per line.
func TestBuildShowsTheCommitTriggerAndTools(t *testing.T) {
	header := func(run core.Run) string {
		run.State, run.StartedAt, run.FinishedAt = core.RunComplete, start, finish
		return Build(query.RunDetail{Run: run, Verdict: core.Verdict{Outcome: core.OutcomePass}}, 3, est).Header
	}

	local := core.Run{
		Source: core.SourceLocal, Environment: "local", Trigger: core.TriggerHook,
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", Dirty: true,
		Tools: []core.Tool{
			{Name: "pgtap", Version: "supabase 2.1.0"},
			{Name: "rest", Version: "unknown: no version command declared"},
		},
	}
	want := "Run 3  PASS\n" +
		"  when         2026-11-11 10:09:32 to 2026-11-11 10:09:38\n" +
		"  source       local, environment local\n" +
		"  commit       0123456 + uncommitted changes\n" +
		"  started by   hook\n" +
		"  tools        pgtap  supabase 2.1.0\n" +
		"               rest   unknown: no version command declared"
	if got := header(local); got != want {
		t.Errorf("header:\n%s\nwant:\n%s", got, want)
	}

	local.Dirty = false
	if got := header(local); !strings.Contains(got, "commit       0123456\n") {
		t.Errorf("a clean checkout shows a note:\n%s", got)
	}

	// An imported file says "not given" when the caller passed no commit, and a
	// pulled run shows neither line: its commit is on the remote run line.
	imported := core.Run{Source: core.SourceImport, Environment: "network"}
	if got := header(imported); !strings.Contains(got, "commit       not given") {
		t.Errorf("an imported run with no commit:\n%s", got)
	}
	remote := core.Run{Source: core.SourceRemote, Environment: "deployed", ExternalID: "9", Attempt: 1, CommitSHA: "abc"}
	got := header(remote)
	if strings.Contains(got, "tools") || strings.Contains(got, "started by") || strings.Contains(got, "commit       ") {
		t.Errorf("a pulled run shows local-only lines:\n%s", got)
	}
}

// A test that printed several checks is one row with its verdict and a count.
// Only its failed checks are listed under it, with their messages; the passing
// ones are counted as hidden until asked for. A test with one check, and a
// result with no test, look as they always did.
func TestBuildOneRowPerTestWithItsFailedChecks(t *testing.T) {
	multi := func(name string, st core.Status, msg string) query.DetailResult {
		r := result("big", name, st, msg, 3)
		r.Feature = "f"
		return r
	}
	d := query.RunDetail{
		Run:     core.Run{Source: core.SourceLocal, Environment: "local", State: core.RunComplete, StartedAt: start, FinishedAt: finish},
		Verdict: core.Verdict{Outcome: core.OutcomeFail, Reason: "1 of 3 tests failed"},
		Results: []query.DetailResult{
			result("solo", "the only check", core.StatusPass, "", 1),
			multi("first", core.StatusPass, ""),
			multi("second", core.StatusFail, "expected 401\ngot 200"),
			multi("third", core.StatusSkip, "no network"),
			result("", "an unlisted thing", core.StatusPass, "", 0),
		},
	}

	got := Build(d, 4, est)
	wantResults := "Results:\n" +
		"  #1   PASS  solo  the only check\n" +
		"  #3   FAIL  big   3 checks, 1 failed, 1 skipped\n" +
		"        FAIL  second\n" +
		"              expected 401\n" +
		"              got 200\n" +
		"  -    PASS  -     an unlisted thing"
	if got.Results != wantResults {
		t.Errorf("results:\n%s\nwant:\n%s", got.Results, wantResults)
	}
	if got.HiddenChecks != 2 || !got.HasHiddenChecks() {
		t.Errorf("HiddenChecks = %d; want the passing and the skipped check (2)", got.HiddenChecks)
	}
	if !strings.Contains(got.Header, "\n  checks       5 in 3 tests, 1 failed") {
		t.Errorf("header lacks the checks line:\n%s", got.Header)
	}

	all := BuildWith(d, 4, est, Options{Checks: true})
	for _, want := range []string{"        PASS  first", "        FAIL  second", "        SKIP  third", "              no network"} {
		if !strings.Contains(all.Results, want) {
			t.Errorf("--checks results lack %q:\n%s", want, all.Results)
		}
	}
	if all.HiddenChecks != 0 || all.HasHiddenChecks() {
		t.Errorf("with every check listed HiddenChecks = %d; want 0", all.HiddenChecks)
	}
}

// When every test printed one check there is nothing to hide, and the header
// has no checks line.
func TestBuildHasNoChecksLineOrHintWhenEveryTestPrintedOne(t *testing.T) {
	d := query.RunDetail{
		Run:     core.Run{Source: core.SourceLocal, State: core.RunComplete, StartedAt: start, FinishedAt: finish},
		Verdict: core.Verdict{Outcome: core.OutcomePass},
		Results: []query.DetailResult{result("a", "A", core.StatusPass, "", 1), result("b", "B", core.StatusPass, "", 2)},
	}
	got := Build(d, 1, est)
	if strings.Contains(got.Header, "checks ") || got.HasHiddenChecks() {
		t.Errorf("header %q, hidden %d; want neither a checks line nor hidden checks", got.Header, got.HiddenChecks)
	}
}

// A failed run with everything a view can hold, compared as a whole.
func TestBuildAFailedRunWithEveryPart(t *testing.T) {
	d := query.RunDetail{
		Run: core.Run{
			Source: core.SourceLocal, Environment: "deployed", State: core.RunComplete,
			StartedAt: start, FinishedAt: finish, ExitCode: 2,
			Diagnostic: "[incubator-soak] timed out after 6s\n# soaking...",
			Detail:     "[hostile-input] hostile requests\nat test.mjs:56",
		},
		Verdict: core.Verdict{Outcome: core.OutcomeFail, Reason: "2 of 3 results failed"},
		Results: []query.DetailResult{
			result("service-health", "the health check answers ok", core.StatusPass, "", 1),
			result("hostile-input", "hostile requests", core.StatusFail, "8 of 63 requests\n  4 x status 200", 5),
			result("", "an unlisted thing", core.StatusError, "  boom  ", 0),
		},
	}
	got := Build(d, 8, est)

	wantHeader := "Run 8  FAIL\n" +
		"  reason       2 of 3 results failed\n" +
		"  when         2026-11-11 10:09:32 to 2026-11-11 10:09:38\n" +
		"  source       local, environment deployed\n" +
		"  exit code    2\n" +
		"  commit       not recorded\n" +
		"  tools        not recorded"
	if got.Header != wantHeader {
		t.Errorf("header:\n%s\nwant:\n%s", got.Header, wantHeader)
	}

	wantResults := "Results:\n" +
		"  #1   PASS  service-health  the health check answers ok\n" +
		"  #5   FAIL  hostile-input   hostile requests\n" +
		"        8 of 63 requests\n" +
		"          4 x status 200\n" +
		"  -    ERROR -               an unlisted thing\n" +
		"        boom"
	if got.Results != wantResults {
		t.Errorf("results:\n%s\nwant:\n%s", got.Results, wantResults)
	}

	if want := "Diagnostic:\n  [incubator-soak] timed out after 6s\n  # soaking..."; got.Diagnostic != want {
		t.Errorf("diagnostic:\n%s\nwant:\n%s", got.Diagnostic, want)
	}
	if want := "Tool detail:\n  [hostile-input] hostile requests\n  at test.mjs:56"; got.Detail != want {
		t.Errorf("detail:\n%s\nwant:\n%s", got.Detail, want)
	}
	if !got.HasDetail() {
		t.Error("HasDetail is false although there is detail")
	}
	for name, part := range map[string]string{"header": got.Header, "results": got.Results, "diagnostic": got.Diagnostic, "detail": got.Detail} {
		if strings.HasSuffix(part, "\n") {
			t.Errorf("the %s ends with a line break; parts must not", name)
		}
	}
}

func TestBuildAPassedRunHasNoDiagnosticNoDetailAndNoReason(t *testing.T) {
	d := query.RunDetail{
		Run:     core.Run{Source: core.SourceLocal, Environment: "local", State: core.RunComplete, StartedAt: start, FinishedAt: finish},
		Verdict: core.Verdict{Outcome: core.OutcomePass},
		Results: []query.DetailResult{result("a", "A", core.StatusPass, "", 1)},
	}
	got := Build(d, 1, time.UTC)
	if strings.Contains(got.Header, "reason") || strings.Contains(got.Header, "exit code") {
		t.Errorf("a passed run shows a reason or exit code:\n%s", got.Header)
	}
	if !strings.HasPrefix(got.Header, "Run 1  PASS") {
		t.Errorf("header:\n%s", got.Header)
	}
	if got.Diagnostic != "" || got.Detail != "" || got.HasDetail() {
		t.Errorf("diagnostic %q, detail %q; want none", got.Diagnostic, got.Detail)
	}
}

func TestBuildARunWithNoResults(t *testing.T) {
	d := query.RunDetail{
		Run:     core.Run{Source: core.SourceLocal, State: core.RunComplete, StartedAt: start, FinishedAt: finish},
		Verdict: core.Verdict{Outcome: core.OutcomeFail, Reason: "no results"},
	}
	got := Build(d, 3, time.UTC)
	if got.Results != "Results:\n  (none)" {
		t.Errorf("results %q; want the none line", got.Results)
	}
	if !strings.Contains(got.Header, "environment -") {
		t.Errorf("an empty environment should show a dash:\n%s", got.Header)
	}
}

func TestBuildARemoteRun(t *testing.T) {
	d := query.RunDetail{
		Run: core.Run{
			Source: core.SourceRemote, Environment: "deployed", State: core.RunComplete,
			StartedAt: start, FinishedAt: finish, ExternalID: "9001", Attempt: 2,
		},
		Verdict: core.Verdict{Outcome: core.OutcomePass},
	}
	if got := Build(d, 4, time.UTC).Header; !strings.Contains(got, "  remote run   9001, attempt 2, commit -") {
		t.Errorf("a remote run with no commit:\n%s", got)
	}
	d.Run.CommitSHA = "deadbeef"
	if got := Build(d, 4, time.UTC).Header; !strings.Contains(got, "commit deadbeef") {
		t.Errorf("a remote run with a commit:\n%s", got)
	}
}

func TestBuildTimesFollowTheZoneAndAnUnsetTimeIsADash(t *testing.T) {
	d := query.RunDetail{
		Run:     core.Run{Source: core.SourceLocal, State: core.RunPending, StartedAt: start},
		Verdict: core.Verdict{Outcome: core.OutcomeUndetermined, Reason: "run still in progress"},
	}
	got := Build(d, 2, time.UTC).Header
	if !strings.Contains(got, "when         2026-11-11 15:09:32 to -") || !strings.HasPrefix(got, "Run 2  PENDING") {
		t.Errorf("a pending run in UTC:\n%s", got)
	}
	if got := Build(d, 2, est).Header; !strings.Contains(got, "2026-11-11 10:09:32") {
		t.Errorf("the same run in EST:\n%s", got)
	}
	if got := Build(d, 2, nil).Header; !strings.Contains(got, "when ") { // the local zone: only that it does not panic
		t.Errorf("a nil zone:\n%s", got)
	}
}

func TestResultWordForEveryStateAndOutcome(t *testing.T) {
	cases := []struct {
		state   core.RunState
		outcome core.Outcome
		want    string
	}{
		{core.RunComplete, core.OutcomePass, "PASS"},
		{core.RunComplete, core.OutcomeFail, "FAIL"},
		{core.RunPending, core.OutcomeUndetermined, "PENDING"},
		{core.RunExpired, core.OutcomeUndetermined, "EXPIRED"},
		{core.RunComplete, core.OutcomeUndetermined, "?"},
	}
	for _, c := range cases {
		if got := ResultWord(c.state, c.outcome); got != c.want {
			t.Errorf("ResultWord(%v, %v) = %q; want %q", c.state, c.outcome, got, c.want)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	if Dash("") != "-" || Dash("x") != "x" {
		t.Error("Dash")
	}
	if got := Indent("a\nb\n\n", "> "); got != "> a\n> b" {
		t.Errorf("Indent = %q; want trailing line breaks dropped and every line prefixed", got)
	}
	if got := When(time.UTC, time.Time{}); got != "-" {
		t.Errorf("When of a zero time = %q", got)
	}
}
