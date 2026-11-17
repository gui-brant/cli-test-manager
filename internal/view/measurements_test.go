package view

import (
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/query"
)

func measured(testID, name string, ms ...core.Measurement) query.DetailResult {
	return query.DetailResult{Result: core.Result{TestID: testID, Name: name, Status: core.StatusPass, Measurements: ms}}
}

func detailOf(run core.Run, results ...query.DetailResult) Run {
	run.State, run.StartedAt, run.FinishedAt = core.RunComplete, start, finish
	return Build(query.RunDetail{Run: run, Verdict: core.Verdict{Outcome: core.OutcomePass}, Results: results}, 1, est)
}

func TestMeasurementsAreTheToolDetail(t *testing.T) {
	r := detailOf(core.Run{Source: core.SourceImport, Environment: "network"},
		measured("", "sign-in p95", core.Measurement{Name: "p95_ms", Value: 187}, core.Measurement{Name: "rate_per_s", Value: 101.5}),
		measured("network-login", "register"), // no numbers: not listed
		measured("network-login", "renew", core.Measurement{Name: "total", Value: 1234567.25}),
	)
	want := "Measurements:\n" +
		"  [-] sign-in p95\n" +
		"      p95_ms      187\n" +
		"      rate_per_s  101.5\n" +
		"  [network-login] renew\n" +
		"      total  1234567.25"
	if r.Detail != want {
		t.Errorf("detail:\n%s\nwant:\n%s", r.Detail, want)
	}
	if !r.HasDetail() {
		t.Error("a run with measurements must offer its detail")
	}
	if wantNote := "The test tool reported measurements (3 numbers on 2 checks)."; r.DetailNote != wantNote {
		t.Errorf("note = %q; want %q", r.DetailNote, wantNote)
	}
	if strings.Contains(r.Results, "p95_ms") || strings.Contains(r.Header, "p95_ms") {
		t.Error("the numbers must show only with the detail, not in the results")
	}
}

func TestMeasurementsComeBeforeStackTraces(t *testing.T) {
	r := detailOf(core.Run{Source: core.SourceLocal, Environment: "local", Detail: "[a] boom\n  at line 1"},
		measured("a", "x", core.Measurement{Name: "n", Value: 1}))
	if !strings.HasPrefix(r.Detail, "Measurements:\n") || !strings.Contains(r.Detail, "\n\nTool detail:\n  [a] boom") {
		t.Errorf("detail:\n%s\nwant the measurements first, then the tool detail", r.Detail)
	}
	if !strings.Contains(r.DetailNote, "measurements (1 number on 1 check)") || !strings.Contains(r.DetailNote, DetailHint) {
		t.Errorf("note = %q; want it to name both kinds of detail", r.DetailNote)
	}
}

func TestStackTracesAloneKeepTheirOldNote(t *testing.T) {
	r := detailOf(core.Run{Source: core.SourceLocal, Environment: "local", Detail: "[a] boom"}, measured("a", "x"))
	if r.DetailNote != DetailHint || strings.Contains(r.Detail, "Measurements:") {
		t.Errorf("note %q, detail %q; want the traces only", r.DetailNote, r.Detail)
	}
}

func TestNoDetailMeansNoNote(t *testing.T) {
	r := detailOf(core.Run{Source: core.SourceLocal, Environment: "local"}, measured("a", "x"))
	if r.HasDetail() || r.Detail != "" || r.DetailNote != "" {
		t.Errorf("detail %q, note %q; want none", r.Detail, r.DetailNote)
	}
}
