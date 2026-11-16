package match

import (
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
)

func testManifest() *core.Manifest {
	return &core.Manifest{
		Tests: []core.TestSpec{
			{ID: "users-table", Name: "Users table", Feature: "accounts", Runner: "sql", File: "tests/sql/users_table.sql"},
			{ID: "login-flow", Name: "Login flow", Feature: "accounts", Runner: "http", File: "tests/http/login_flow.http"},
			{ID: "note-create", Name: "Create a note", Feature: "notes", Runner: "http", File: "tests/http/note_create.http"},
		},
	}
}

func TestMatchingByIdThenByFilePath(t *testing.T) {
	find := Tests(testManifest())
	for group, want := range map[string]string{
		"login-flow":                              "login-flow",  // the id
		"tests/http/login_flow.http":              "login-flow",  // the path
		"ci/work/repo/tests/http/login_flow.http": "login-flow",  // a longer path ends with it
		"./tests/http/note_create.http":           "note-create", // a leading ./
		`tests\sql\users_table.sql`:               "users-table", // backslashes
		"  note-create  ":                         "note-create", // spaces
	} {
		got, ok := find(group)
		if !ok || got.ID != want {
			t.Errorf("%q matched %q (found %v); want %q", group, got.ID, ok, want)
		}
	}
	for _, group := range []string{"", "stray-suite", "login_flow.http", "other/tests/http/login_flow.http.bak", "users"} {
		if got, ok := find(group); ok {
			t.Errorf("%q matched %q; want no test", group, got.ID)
		}
	}
}

func TestTheLongestPathWinsWhenSeveralMatch(t *testing.T) {
	m := &core.Manifest{Tests: []core.TestSpec{
		{ID: "short", File: "b.http"},
		{ID: "long", File: "api/b.http"},
	}}
	got, ok := Tests(m)("repo/api/b.http")
	if !ok || got.ID != "long" {
		t.Errorf("matched %q; want long", got.ID)
	}
}

func TestOnlyGivesEverythingToOneTest(t *testing.T) {
	find := Only(core.TestSpec{ID: "load-ramp", Feature: "general"})
	for _, group := range []string{"", "anything", "login-flow"} {
		if got, ok := find(group); !ok || got.ID != "load-ramp" {
			t.Errorf("%q matched %q (found %v); want load-ramp", group, got.ID, ok)
		}
	}
}

func TestResultsAreGivenTheirTestAndFeatureAndUnmatchedOnesAreCounted(t *testing.T) {
	items := []parse.Item{
		{Group: "login-flow", Result: core.Result{Name: "signs in", Status: core.StatusPass}},
		{Group: "tests/http/note_create.http", Result: core.Result{Name: "creates", Status: core.StatusFail, Message: "got 500"}, Detail: "stack line 1"},
		{Group: "stray", Result: core.Result{Name: "unlisted", Status: core.StatusFail}, Detail: "stray detail"},
		{Result: core.Result{Name: "no group", Status: core.StatusSkip}},
	}
	results, detail, unmatched := Results(items, Tests(testManifest()))

	if unmatched != 2 || len(results) != 4 {
		t.Fatalf("%d results, %d unmatched; want 4 and 2", len(results), unmatched)
	}
	if r := results[0]; r.TestID != "login-flow" || r.Feature != "accounts" {
		t.Errorf("first result %+v", r)
	}
	if r := results[1]; r.TestID != "note-create" || r.Feature != "notes" || r.Message != "got 500" {
		t.Errorf("second result %+v", r)
	}
	if r := results[2]; r.TestID != "" || r.Feature != "" {
		t.Errorf("an unmatched result was given a test: %+v", r)
	}
	// The detail of a failure is labelled with its test id, or its group when it
	// matched none.
	for _, want := range []string{"[note-create] creates\nstack line 1", "[stray] unlisted\nstray detail"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not contain %q", detail, want)
		}
	}
}

func TestResultsWithOnlyGiveEveryResultTheOneTest(t *testing.T) {
	items := []parse.Item{
		{Group: "a", Result: core.Result{Name: "one", Status: core.StatusPass}},
		{Group: "b", Result: core.Result{Name: "two", Status: core.StatusFail}},
	}
	results, _, unmatched := Results(items, Only(core.TestSpec{ID: "load-ramp", Feature: "general"}))
	if unmatched != 0 {
		t.Errorf("%d unmatched; want 0", unmatched)
	}
	for _, r := range results {
		if r.TestID != "load-ramp" || r.Feature != "general" {
			t.Errorf("result %+v; want load-ramp in general", r)
		}
	}
}
