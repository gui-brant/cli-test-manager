package importer

import (
	"context"
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
	bg   = context.Background()
	when = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
)

type fakeCreds map[string]string

func (c fakeCreds) Get(_ context.Context, name string) (string, error) {
	if v, ok := c[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("fake: %s: %w", name, core.ErrNotFound)
}

func testManifest() *core.Manifest {
	return &core.Manifest{
		Features: map[string]core.FeatureSpec{"login": {}},
		Environments: map[string]core.EnvironmentSpec{
			"local":   {Actions: []core.Action{core.ActionRun}},
			"network": {Actions: []core.Action{core.ActionImport}, Secrets: []string{"LAB_TOKEN"}},
			"lab2":    {Actions: []core.Action{core.ActionImport}},
		},
		Tests: []core.TestSpec{
			{ID: "load-ramp", Name: "Load ramp", Feature: "general", Runner: "k6", File: "tests/load/ramp.js"},
			{ID: "login-flow", Name: "Login flow", Feature: "login", Runner: "k6", File: "tests/load/login.js"},
		},
	}
}

func newService(t *testing.T, creds core.Credentials) (*Service, *sqlite.Store) {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{Store: st, Creds: creds, Now: func() time.Time { return when.Add(48 * time.Hour) }}, st
}

const (
	tapFile = "1..2\nok 1 - p95 under 450ms\nnot ok 2 - error rate under 1%\n"
	xmlFile = `<testsuites>
  <testsuite name="load-ramp"><testcase classname="load-ramp" name="p95 under 450ms" time="1.5"/></testsuite>
  <testsuite name="tests/load/login.js"><testcase classname="tests/load/login.js" name="sign-in rate"><failure message="too slow"/></testcase></testsuite>
  <testsuite name="stray"><testcase classname="stray" name="who is this"/></testsuite>
</testsuites>`
)

func request(data string, format core.ResultFormat) Request {
	return Request{Manifest: testManifest(), Environment: "network", Data: []byte(data), Format: format, When: when}
}

func stored(t *testing.T, st *sqlite.Store, id int64) core.Run {
	t.Helper()
	r, err := st.GetRun(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runCount(t *testing.T, st *sqlite.Store) int {
	t.Helper()
	runs, err := st.ListRuns(bg, core.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

func TestImportingATAPFileForOneTest(t *testing.T) {
	s, st := newService(t, nil)
	req := request(tapFile, core.FormatTAP)
	req.TestID = "load-ramp"
	req.Commit = "cafe1234"
	req.Tools = []core.Tool{{Name: "k6", Version: "k6 v1.4.0"}}

	rep, err := s.Import(bg, req)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Already || rep.RunID == 0 || rep.Unmatched != 0 {
		t.Fatalf("report %+v", rep)
	}
	got := stored(t, st, rep.RunID)
	if got.Source != core.SourceImport || got.Environment != "network" || got.State != core.RunComplete ||
		!got.StartedAt.Equal(when) || !got.FinishedAt.Equal(when) || got.CommitSHA != "cafe1234" || got.Attempt != 1 {
		t.Errorf("run fields %+v", got)
	}
	if !reflect.DeepEqual(got.Tools, []core.Tool{{Name: "k6", Version: "k6 v1.4.0"}}) {
		t.Errorf("tools %+v", got.Tools)
	}
	if len(got.Results) != 2 || got.Results[0].TestID != "load-ramp" || got.Results[1].TestID != "load-ramp" ||
		got.Results[0].Feature != "general" || got.Results[1].Status != core.StatusFail {
		t.Errorf("results %+v; want both given to load-ramp, the second failing", got.Results)
	}
	// Importing records; it does not judge. The run itself shows the failure.
	if got.Verdict().Outcome != core.OutcomeFail {
		t.Errorf("verdict %v; want the run to show fail", got.Verdict().Outcome)
	}
}

func TestImportingAJUnitFileMatchesResultsByNameAndKeepsUnmatchedOnes(t *testing.T) {
	s, st := newService(t, nil)
	rep, err := s.Import(bg, request(xmlFile, core.FormatJUnit))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unmatched != 1 {
		t.Errorf("%d unmatched; want 1 (the stray suite)", rep.Unmatched)
	}
	got := stored(t, st, rep.RunID)
	if len(got.Results) != 3 {
		t.Fatalf("%d results; want all 3 kept", len(got.Results))
	}
	if r := got.Results[0]; r.TestID != "load-ramp" || r.Feature != "general" {
		t.Errorf("matched by id: %+v", r)
	}
	if r := got.Results[1]; r.TestID != "login-flow" || r.Feature != "login" || r.Status != core.StatusFail || r.Message != "too slow" {
		t.Errorf("matched by file path: %+v", r)
	}
	if r := got.Results[2]; r.TestID != "" || r.Feature != "" {
		t.Errorf("an unmatched result was given a test: %+v", r)
	}
}

func TestNothingIsGuessedAboutTheCommitOrTheTools(t *testing.T) {
	s, st := newService(t, nil)
	rep, err := s.Import(bg, request(tapFile, core.FormatTAP))
	if err != nil {
		t.Fatal(err)
	}
	got := stored(t, st, rep.RunID)
	if got.CommitSHA != "" || len(got.Tools) != 0 || got.Dirty || got.Trigger != "" {
		t.Errorf("commit %q, tools %v, dirty %v, trigger %q; want none given", got.CommitSHA, got.Tools, got.Dirty, got.Trigger)
	}
}

func TestWithNoWhenTheMomentOfImportIsUsed(t *testing.T) {
	s, st := newService(t, nil)
	req := request(tapFile, core.FormatTAP)
	req.When = time.Time{}
	rep, err := s.Import(bg, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored(t, st, rep.RunID); !got.StartedAt.Equal(when.Add(48 * time.Hour)) {
		t.Errorf("when = %v; want the service's now", got.StartedAt)
	}
}

func TestTheSameFileIntoTheSameEnvironmentIsStoredOnce(t *testing.T) {
	s, st := newService(t, nil)
	first, err := s.Import(bg, request(tapFile, core.FormatTAP))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Import(bg, request(tapFile, core.FormatTAP))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Already || again.RunID != first.RunID {
		t.Errorf("second import %+v; want already imported as run %d", again, first.RunID)
	}
	if n := runCount(t, st); n != 1 {
		t.Errorf("%d runs stored; want 1", n)
	}

	// Another environment, or a changed file, is a new run.
	other := request(tapFile, core.FormatTAP)
	other.Environment = "lab2"
	if rep, err := s.Import(bg, other); err != nil || rep.Already {
		t.Errorf("same file into another environment: %+v, %v", rep, err)
	}
	if rep, err := s.Import(bg, request(tapFile+"ok 3 - one more\n", core.FormatTAP)); err != nil || rep.Already {
		t.Errorf("a changed file: %+v, %v", rep, err)
	}
	if n := runCount(t, st); n != 3 {
		t.Errorf("%d runs stored; want 3", n)
	}
}

func TestAnEnvironmentThatCannotImportIsRefusedAndNamesThoseThatCan(t *testing.T) {
	s, st := newService(t, nil)
	for env, want := range map[string]string{
		"local":   `environment "local" does not allow import (those that do: lab2, network)`,
		"nowhere": `environment "nowhere" is not defined (defined: lab2, local, network)`,
	} {
		req := request(tapFile, core.FormatTAP)
		req.Environment = env
		if _, err := s.Import(bg, req); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v; want it to contain %q", env, err, want)
		}
	}

	m := testManifest()
	delete(m.Environments, "network")
	delete(m.Environments, "lab2")
	req := request(tapFile, core.FormatTAP)
	req.Manifest, req.Environment = m, "local"
	if _, err := s.Import(bg, req); err == nil || !strings.Contains(err.Error(), "no environment does") {
		t.Errorf("no environment imports: %v", err)
	}
	if n := runCount(t, st); n != 0 {
		t.Errorf("%d runs stored by refused imports", n)
	}
	if got := ImportEnvironments(testManifest()); !reflect.DeepEqual(got, []string{"lab2", "network"}) {
		t.Errorf("ImportEnvironments = %v", got)
	}
}

// A request or a file that cannot be used stores nothing.
func TestBadRequestsAndFilesStoreNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"an unknown test", func(r *Request) { r.TestID = "nope" }, `test "nope" is not in the manifest`},
		{"an unknown format", func(r *Request) { r.Format = "csv" }, `format "csv"`},
		{"broken XML", func(r *Request) { r.Format, r.Data = core.FormatJUnit, []byte("<testsuite><testcase") }, "could not be read as junit"},
		{"text that is not TAP", func(r *Request) { r.Data = []byte("hello\nworld\n") }, "no results found in the file; is it tap?"},
		{"an empty file", func(r *Request) { r.Data = nil }, "no results found"},
		{"a tool with no version", func(r *Request) { r.Tools = []core.Tool{{Name: "k6"}} }, "a tool needs a name and a version"},
		{"a file that is too large", func(r *Request) { r.Data = make([]byte, MaxFileBytes+1) }, "larger than 32 MB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, st := newService(t, nil)
			req := request(tapFile, core.FormatTAP)
			c.mutate(&req)
			if _, err := s.Import(bg, req); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v; want it to contain %q", err, c.want)
			}
			if n := runCount(t, st); n != 0 {
				t.Errorf("%d runs stored", n)
			}
		})
	}
}

// A file from outside may carry a secret of the environment; it must not reach
// the history. A secret that is not set is no reason to refuse the file.
func TestSecretsOfTheEnvironmentAreScrubbed(t *testing.T) {
	file := "1..1\nnot ok 1 - login with token lab-secret-9876 failed\n"

	s, st := newService(t, fakeCreds{"LAB_TOKEN": "lab-secret-9876"})
	rep, err := s.Import(bg, request(file, core.FormatTAP))
	if err != nil {
		t.Fatal(err)
	}
	if name := stored(t, st, rep.RunID).Results[0].Name; strings.Contains(name, "lab-secret-9876") || !strings.Contains(name, "[redacted]") {
		t.Errorf("name %q; want the secret scrubbed", name)
	}

	s, st = newService(t, fakeCreds{}) // the secret is not set
	rep, err = s.Import(bg, request(file, core.FormatTAP))
	if err != nil {
		t.Fatalf("an unset secret refused the file: %v", err)
	}
	if name := stored(t, st, rep.RunID).Results[0].Name; !strings.Contains(name, "lab-secret-9876") {
		t.Errorf("name %q; nothing to scrub with, so it stays as written", name)
	}

	s, _ = newService(t, fakeCreds{"LAB_TOKEN": "ab"}) // too short to scrub safely
	if _, err := s.Import(bg, request(file, core.FormatTAP)); err == nil || !strings.Contains(err.Error(), "LAB_TOKEN") {
		t.Errorf("a too-short secret: %v; want an error naming it", err)
	}
}

// Preparing needs no history file at all: a service with no store can check,
// scrub and read a file, and refuses a bad one. That is what lets the command
// refuse a request before it creates the file.
func TestPrepareNeedsNoStore(t *testing.T) {
	s := &Service{Now: func() time.Time { return when }}
	if _, err := s.Prepare(bg, request(tapFile, core.FormatTAP)); err != nil {
		t.Errorf("a sound file was refused: %v", err)
	}
	for name, mutate := range map[string]func(*Request){
		"a bad environment":      func(r *Request) { r.Environment = "local" },
		"a file with no results": func(r *Request) { r.Data = []byte("nothing here") },
	} {
		req := request(tapFile, core.FormatTAP)
		mutate(&req)
		if _, err := s.Prepare(bg, req); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestACancelledContextStoresNothing(t *testing.T) {
	s, st := newService(t, nil)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := s.Import(ctx, request(tapFile, core.FormatTAP)); err == nil {
		t.Error("a cancelled import succeeded")
	}
	if n := runCount(t, st); n != 0 {
		t.Errorf("%d runs stored", n)
	}
}

func TestFormatFromTheFileName(t *testing.T) {
	for name, want := range map[string]core.ResultFormat{
		"results.xml": core.FormatJUnit, "RESULTS.XML": core.FormatJUnit,
		"out/run.tap": core.FormatTAP, "a.b.tap": core.FormatTAP,
	} {
		if got, ok := FormatFor(name); !ok || got != want {
			t.Errorf("%q: %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"results", "results.json", "xml", "results.txt", ""} {
		if got, ok := FormatFor(name); ok {
			t.Errorf("%q gave %q; want no guess", name, got)
		}
	}
}
