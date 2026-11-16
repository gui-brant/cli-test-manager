package execute

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// changeManifest is the usual manifest with patterns on both features, and one
// test in general.
func changeManifest(f *fixture) *core.Manifest {
	m := f.manifest
	m.Features["accounts"] = core.FeatureSpec{Paths: []string{"db/**/*.sql", "db/shared/**"}}
	m.Features["notes"] = core.FeatureSpec{Paths: []string{"docs/**", "db/shared/**"}}
	m.Tests = append(m.Tests, core.TestSpec{ID: "smoke", Name: "Smoke", Feature: core.GeneralFeature, Runner: "http", File: "tests/http/smoke.http"})
	return m
}

func ids(tests []core.TestSpec) []string {
	var out []string
	for _, t := range tests {
		out = append(out, t.ID)
	}
	return out
}

func skippedIDs(skipped []SkippedTest) []string {
	var out []string
	for _, s := range skipped {
		out = append(out, s.Test.ID)
	}
	return out
}

func TestChangedFilesSelectFeatures(t *testing.T) {
	m := changeManifest(newFixture(t))
	cases := []struct {
		name         string
		manifestFile string
		changed      []string
		want         []string
		files        int
		outside      int
		inNoFeature  int
		manifest     bool
	}{
		{name: "a file a pattern matches", changed: []string{"db/migrations/001.sql"}, want: []string{"accounts"}, files: 1},
		{name: "two features", changed: []string{"db/a.sql", "docs/x.md"}, want: []string{"accounts", "notes"}, files: 2},
		{name: "a file two features match", changed: []string{"db/shared/x.txt"}, want: []string{"accounts", "notes"}, files: 1},
		{name: "a test's own file that no pattern matches", changed: []string{"tests/http/login_flow.http"}, want: []string{"accounts"}, files: 1},
		{name: "a general test's file", changed: []string{"tests/http/smoke.http"}, want: []string{"general"}, files: 1},
		{name: "a file in no feature", changed: []string{"README.md"}, want: []string{"general"}, files: 1, inNoFeature: 1},
		{name: "no feature and a feature together", changed: []string{"README.md", "docs/x.md"}, want: []string{"general", "notes"}, files: 2, inNoFeature: 1},
		{name: "the manifest selects everything", changed: []string{"test-manifest.yaml"}, want: []string{"accounts", "general", "notes"}, files: 1, manifest: true},
		{name: "a manifest by another name", manifestFile: "my.yaml", changed: []string{"my.yaml"}, want: []string{"accounts", "general", "notes"}, files: 1, manifest: true},
		{name: "a file merely named like the manifest elsewhere", changed: []string{"other/test-manifest.yaml"}, want: []string{"general"}, files: 1, inNoFeature: 1},
		{name: "outside the project is ignored", changed: []string{"/etc/passwd", "../x", "a/../../y", "", "   "}, want: nil, outside: 3},
		{name: "spellings of one path are one file", changed: []string{"./db/a.sql", "db\\a.sql", "db/a.sql", "db//a.sql"}, want: []string{"accounts"}, files: 1},
		{name: "nothing changed", changed: nil, want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set, sum := featuresOfChanges(m, c.manifestFile, c.changed)
			if !reflect.DeepEqual(sum.Features, c.want) {
				t.Errorf("features = %v; want %v", sum.Features, c.want)
			}
			if len(set) != len(c.want) {
				t.Errorf("set %v does not match the summary %v", set, sum.Features)
			}
			if sum.Files != c.files || sum.Outside != c.outside || sum.InNoFeature != c.inNoFeature || sum.Manifest != c.manifest {
				t.Errorf("summary = %+v; want files %d, outside %d, in no feature %d, manifest %v", sum, c.files, c.outside, c.inNoFeature, c.manifest)
			}
		})
	}
}

func previewOf(t *testing.T, f *fixture, sel Selection) Preview {
	t.Helper()
	p, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: sel})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChangedFilesNarrowTheRunLikeAFeatureDoes(t *testing.T) {
	f := newFixture(t)
	changeManifest(f)

	p := previewOf(t, f, Selection{UseChanged: true, Changed: []string{"docs/a.md"}})
	if got := ids(p.Tests); !reflect.DeepEqual(got, []string{"note-create"}) || p.Changed == nil || !reflect.DeepEqual(p.Changed.Features, []string{"notes"}) {
		t.Errorf("tests %v, changed %+v; want only note-create", got, p.Changed)
	}
	// Nothing was listed as skipped: tests of unselected features are simply not
	// chosen, as with --feature.
	if len(p.Skipped) != 0 {
		t.Errorf("skipped %+v; unselected features are not listed", p.Skipped)
	}

	// A file in no feature runs the general tests.
	if got := ids(previewOf(t, f, Selection{UseChanged: true, Changed: []string{"README.md"}}).Tests); !reflect.DeepEqual(got, []string{"smoke"}) {
		t.Errorf("README.md selects %v; want the general test", got)
	}

	// The manifest runs everything that can run.
	if got := ids(previewOf(t, f, Selection{UseChanged: true, Changed: []string{"test-manifest.yaml"}}).Tests); len(got) != 4 {
		t.Errorf("the manifest selects %v; want all 4 tests", got)
	}

	// With the other conditions it is an AND.
	for name, c := range map[string]struct {
		sel  Selection
		want []string
	}{
		"feature agrees":  {Selection{UseChanged: true, Changed: []string{"db/a.sql"}, Feature: "accounts"}, []string{"users-table", "login-flow"}},
		"feature differs": {Selection{UseChanged: true, Changed: []string{"db/a.sql"}, Feature: "notes"}, nil},
		"test agrees":     {Selection{UseChanged: true, Changed: []string{"db/a.sql"}, TestIDs: []string{"login-flow"}}, []string{"login-flow"}},
		"test differs":    {Selection{UseChanged: true, Changed: []string{"docs/a.md"}, TestIDs: []string{"login-flow"}}, nil},
	} {
		if got := ids(previewOf(t, f, c.sel).Tests); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v; want %v", name, got, c.want)
		}
	}
}

// Nothing changed is not a failure: nothing is started or saved.
func TestNothingChangedIsNothingToRun(t *testing.T) {
	f := newFixture(t)
	changeManifest(f)
	for name, changed := range map[string][]string{"no files": nil, "only outside the project": {"../elsewhere"}} {
		p := previewOf(t, f, Selection{UseChanged: true, Changed: changed})
		if len(p.Tests) != 0 || p.Changed == nil || len(p.Changed.Features) != 0 {
			t.Errorf("%s: tests %v, changed %+v", name, ids(p.Tests), p.Changed)
		}
		rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local",
			Select: Selection{UseChanged: true, Changed: changed}})
		if err != nil || !rep.NothingToRun || rep.Saved || rep.RunID != 0 {
			t.Errorf("%s: report %+v, error %v; want nothing to run, nothing saved", name, rep, err)
		}
	}
	if len(f.runner.calls) != 0 || f.storedRuns() != 0 {
		t.Errorf("%d commands started, %d runs stored; want none", len(f.runner.calls), f.storedRuns())
	}
	// A mistaken request is still an error.
	if _, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Feature: "general", TestIDs: []string{"login-flow"}}}); err == nil {
		t.Error("a test outside the named feature was accepted")
	}
}

// A feature whose tests cannot run in the environment is still the environment's
// problem, narrowing or not.
func TestChangedFeaturesThatCannotRunHereStillReportWhy(t *testing.T) {
	f := newFixture(t)
	changeManifest(f)
	f.manifest.Features["accounts"] = core.FeatureSpec{Paths: []string{"db/**"}}
	f.manifest.Tests = f.manifest.Tests[:1] // users-table only: runner sql, local only
	_, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "deployed",
		Select: Selection{UseChanged: true, Changed: []string{"db/a.sql"}}})
	if err == nil || !strings.Contains(err.Error(), "nothing to run: all 1 selected tests were skipped") {
		t.Errorf("error = %v; want the environment reason", err)
	}
}

// ---- never played ----

// playedFixture has every test's file in a tree and has run them all once, all
// passing, so the history knows their files.
func playedFixture(t *testing.T) (*fixture, *fakeTree) {
	t.Helper()
	f := newFixture(t)
	changeManifest(f)
	tr := newTree(true, map[string]string{
		"tests/sql/users_table.sql":   "select 1;",
		"tests/http/login_flow.http":  "GET /login",
		"tests/http/note_create.http": "POST /notes",
		"tests/http/smoke.http":       "GET /",
	})
	f.svc.Tree = tr
	rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local"})
	if err != nil || !rep.Saved || rep.Run.Verdict().Outcome != core.OutcomePass {
		t.Fatalf("the first run: %+v, %v", rep.Run.Verdict(), err)
	}
	f.runner.calls = nil
	return f, tr
}

func TestUnplayedLeavesOutTestsThatPassedWithTheirCurrentFile(t *testing.T) {
	f, _ := playedFixture(t)

	var events []Event
	rep, err := f.svc.Run(bg, Request{
		Manifest: f.manifest, Root: "/project", Environment: "local",
		Select:   Selection{Unplayed: true},
		Progress: func(e Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.NothingToRun || rep.Saved || len(rep.Skipped) != 4 || len(f.runner.calls) != 0 || f.storedRuns() != 1 {
		t.Fatalf("report %+v; commands %d, runs %d; want nothing to run, 4 left out, nothing new", rep, len(f.runner.calls), f.storedRuns())
	}
	for _, sk := range rep.Skipped {
		if sk.Reason != "already passed in local with this file (run 1)" {
			t.Errorf("%s: reason %q", sk.Test.ID, sk.Reason)
		}
	}
	// Each one was reported as it happens, as any other skipped test is.
	if len(events) != 4 || events[0].Kind != EventSkipped {
		t.Errorf("events %+v; want four skipped events", events)
	}
}

func TestUnplayedRunsATestWhoseFileChanged(t *testing.T) {
	f, tr := playedFixture(t)
	tr.set("tests/http/login_flow.http", "GET /login/v2")

	rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Unplayed: true}})
	if err != nil || rep.NothingToRun || !rep.Saved {
		t.Fatalf("report %+v, %v", rep, err)
	}
	var ran []string
	for _, r := range rep.Run.Results {
		ran = append(ran, r.TestID)
	}
	if !reflect.DeepEqual(ran, []string{"login-flow"}) || len(rep.Skipped) != 3 {
		t.Errorf("ran %v with %d left out; want only login-flow and 3 left out", ran, len(rep.Skipped))
	}

	// And it is now played: the same file again runs nothing, and says which run
	// passed it last.
	rep, err = f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Unplayed: true}})
	if err != nil || !rep.NothingToRun {
		t.Fatalf("the second time: %+v, %v", rep, err)
	}
	for _, sk := range rep.Skipped {
		want := "(run 1)"
		if sk.Test.ID == "login-flow" {
			want = "(run 2)"
		}
		if !strings.HasSuffix(sk.Reason, want) {
			t.Errorf("%s: reason %q; want it to end with %s", sk.Test.ID, sk.Reason, want)
		}
	}
}

// A test that failed is not played, even if its file has not changed.
func TestUnplayedRunsAgainATestThatFailed(t *testing.T) {
	f := newFixture(t)
	changeManifest(f)
	f.svc.Tree = newTree(true, map[string]string{
		"tests/sql/users_table.sql": "select 1;", "tests/http/login_flow.http": "GET /login",
		"tests/http/note_create.http": "POST /notes", "tests/http/smoke.http": "GET /",
	})
	f.runner.reply = func(n int, inv core.Invocation) (core.Output, error) {
		if inv.Args[0] == "http-runner" {
			return core.Output{Report: []byte(`<testsuite name="s"><testcase name="c"><failure message="no"/></testcase></testsuite>`)}, nil
		}
		return defaultReply(n, inv)
	}
	if rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local"}); err != nil || rep.Run.Verdict().Outcome != core.OutcomeFail {
		t.Fatalf("the first run should fail: %+v, %v", rep.Run.Verdict(), err)
	}
	f.runner.reply = defaultReply // fixed now
	f.runner.calls = nil

	rep, err := f.svc.Run(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Unplayed: true}})
	if err != nil || !rep.Saved {
		t.Fatalf("report %+v, %v", rep, err)
	}
	// users-table passed and is left out; the three http tests failed, so they run.
	if got := skippedIDs(rep.Skipped); !reflect.DeepEqual(got, []string{"users-table"}) {
		t.Errorf("left out %v; want only users-table", got)
	}
	var ran []string
	for _, r := range rep.Run.Results {
		ran = append(ran, r.TestID)
	}
	if !reflect.DeepEqual(ran, []string{"login-flow", "note-create", "smoke"}) {
		t.Errorf("ran %v; want the three failed tests again", ran)
	}
}

// Passing in one environment says nothing about another.
func TestUnplayedLooksOnlyAtTheChosenEnvironment(t *testing.T) {
	f, _ := playedFixture(t) // all passed in local
	p, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "deployed", Select: Selection{Unplayed: true}})
	if err != nil {
		t.Fatal(err)
	}
	// The http tests serve deployed too; the sql test cannot run there, and is
	// reported for that reason, not as played.
	if got := ids(p.Tests); !reflect.DeepEqual(got, []string{"login-flow", "note-create", "smoke"}) {
		t.Errorf("tests %v; want the three that can run in deployed", got)
	}
	if got := skippedIDs(p.Skipped); !reflect.DeepEqual(got, []string{"users-table"}) || !strings.Contains(p.Skipped[0].Reason, "runs only in") {
		t.Errorf("left out %v (%v); want users-table, for its runner", got, p.Skipped)
	}
}

func TestUnplayedWithNoHistoryRunsEverything(t *testing.T) {
	f := newFixture(t)
	changeManifest(f)
	f.svc.Tree = newTree(true, map[string]string{"tests/sql/users_table.sql": "x", "tests/http/login_flow.http": "x", "tests/http/note_create.http": "x", "tests/http/smoke.http": "x"})
	f.svc.Store = nil // a preview before any history file exists
	p := previewOf(t, f, Selection{Unplayed: true})
	if len(p.Tests) != 4 || len(p.Skipped) != 0 {
		t.Errorf("tests %v, skipped %v; want all four to run", ids(p.Tests), p.Skipped)
	}
}

func TestUnplayedKeepsATestWhoseFileCannotBeRead(t *testing.T) {
	f, tr := playedFixture(t)
	tr.readErr["tests/http/note_create.http"] = errors.New("permission denied")
	p := previewOf(t, f, Selection{Unplayed: true})
	if got := ids(p.Tests); !reflect.DeepEqual(got, []string{"note-create"}) {
		t.Errorf("tests %v; want the unreadable one, to fail with its own error", got)
	}
}

func TestUnplayedNeedsToReadTheTestFiles(t *testing.T) {
	f := newFixture(t) // no Tree
	_, err := f.svc.Preview(bg, Request{Manifest: f.manifest, Root: "/project", Environment: "local", Select: Selection{Unplayed: true}})
	if err == nil || !strings.Contains(err.Error(), "needs to read the test files") {
		t.Errorf("error = %v", err)
	}
}

// --changed and --unplayed together: what changed, and only what never passed.
func TestChangedAndUnplayedTogether(t *testing.T) {
	f, tr := playedFixture(t)
	tr.set("tests/http/note_create.http", "POST /notes/v2") // a new version of a notes test
	p := previewOf(t, f, Selection{UseChanged: true, Changed: []string{"docs/a.md", "db/a.sql"}, Unplayed: true})
	// notes and accounts are selected; only the changed test is unplayed.
	if got := ids(p.Tests); !reflect.DeepEqual(got, []string{"note-create"}) {
		t.Errorf("tests %v; want only note-create", got)
	}
	if len(p.Skipped) != 2 {
		t.Errorf("left out %v; want the two accounts tests", p.Skipped)
	}
	// A change in a feature whose tests all passed runs nothing.
	p = previewOf(t, f, Selection{UseChanged: true, Changed: []string{"db/a.sql"}, Unplayed: true})
	if len(p.Tests) != 0 || len(p.Skipped) != 2 {
		t.Errorf("tests %v, left out %d; want nothing to run", ids(p.Tests), len(p.Skipped))
	}
}
