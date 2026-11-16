package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

var runStart = time.Date(2026, 11, 11, 15, 9, 32, 0, time.UTC)

// historyMenu opens the menu on one project with this manifest. save stores a run
// in its history file, which is created by the first save and not before.
func historyMenu(t *testing.T, manifest string) (m *Model, historyFile string, save func(core.Run) int64) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test-manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &projects.Service{File: filepath.Join(t.TempDir(), "tester", "projects.yaml")}
	if _, _, err := svc.Add(dir, "", nil); err != nil {
		t.Fatal(err)
	}
	historyFile = filepath.Join(dir, ".tester", "history.sqlite")
	open := func(path string) (core.Store, error) {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return sqlite.Open(path)
	}
	save = func(r core.Run) int64 {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(historyFile), 0o755); err != nil {
			t.Fatal(err)
		}
		s, err := sqlite.Open(historyFile)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		id, err := s.SaveRun(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	m = New(Deps{Projects: svc, Load: manifestfile.Load, History: open, Loc: time.UTC})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m, historyFile, save
}

func aRun(env string, minutes int, results ...core.Result) core.Run {
	started := runStart.Add(time.Duration(minutes) * time.Minute)
	return core.Run{
		Source: core.SourceLocal, Environment: env, State: core.RunComplete,
		StartedAt: started, FinishedAt: started.Add(5 * time.Second), Results: results,
	}
}

func aResult(id, feature string, status core.Status, message string) core.Result {
	return core.Result{TestID: id, Feature: feature, Name: "the " + id + " check", Status: status, Message: message}
}

// toHistory goes project › birds › local › Run history.
func toHistory(m *Model) { press(m, "enter", "enter", "enter", "enter") }

func TestRunHistoryWithNoHistoryFileSaysSoAndCreatesNothing(t *testing.T) {
	m, file, _ := historyMenu(t, twoEnvironments)
	toHistory(m)
	if _, ok := top(m).(*historyScreen); !ok {
		t.Fatalf("top screen is %T; want the run history", top(m))
	}
	mustContain(t, "history", m.View(), "photo › birds › local", "Runs of birds in local, newest first", "No runs of birds in local yet.")
	if _, err := os.Stat(file); err == nil {
		t.Error("reading the history created the history file")
	}
}

func TestRunHistoryListsTheRunsOfTheChosenFeatureAndEnvironmentNewestFirst(t *testing.T) {
	m, _, save := historyMenu(t, twoEnvironments)
	save(aRun("local", 0, aResult("t1", "birds", core.StatusPass, "")))                                                  // run 1
	save(aRun("local", 1, aResult("t1", "birds", core.StatusFail, "boom"), aResult("t2", "birds", core.StatusPass, ""))) // run 2
	save(aRun("staging", 2, aResult("t1", "birds", core.StatusPass, "")))                                                // run 3: another environment
	save(aRun("local", 3, aResult("t3", "security", core.StatusPass, "")))                                               // run 4: another feature
	toHistory(m)

	view := m.View()
	mustContain(t, "history", view, "Run 2", "FAIL", "1/2 failed", "Run 1", "PASS", "0/1 failed", "2026-11-11 15:10:32", "local")
	for _, other := range []string{"Run 3", "Run 4"} {
		if strings.Contains(view, other) {
			t.Errorf("%s is not a run of birds in local but is listed:\n%s", other, view)
		}
	}
	if strings.Index(view, "Run 2") > strings.Index(view, "Run 1") {
		t.Errorf("the newest run should come first:\n%s", view)
	}
	mustContain(t, "history", view, "> Run 2")
}

func TestOpeningARunShowsItsPartsAndEscComesBackToTheSameRow(t *testing.T) {
	m, _, save := historyMenu(t, twoEnvironments)
	save(aRun("local", 0, aResult("t1", "birds", core.StatusPass, "")))
	failed := aRun("local", 1, aResult("t1", "birds", core.StatusFail, "expected 3, got 4"), aResult("t2", "birds", core.StatusPass, ""))
	failed.Diagnostic = "[t1] the command printed this"
	save(failed)
	toHistory(m)
	press(m, "down", "enter") // run 1 (second row)
	mustContain(t, "run", m.View(), "photo › birds › local › run 1", "Run 1  PASS", "Results:", "#1", "t1")

	press(m, "esc")
	mustContain(t, "history", m.View(), "Runs of birds in local", "> Run 1")

	press(m, "up", "enter") // run 2, the failed one
	mustContain(t, "run", m.View(), "Run 2  FAIL", "reason       1 of 2 tests failed", "expected 3, got 4",
		"Diagnostic:", "[t1] the command printed this")
}

func TestTheDetailKeyShowsAndHidesTheToolsDetailOnlyWhenThereIsSome(t *testing.T) {
	m, _, save := historyMenu(t, twoEnvironments)
	save(aRun("local", 0, aResult("t1", "birds", core.StatusPass, "")))
	withDetail := aRun("local", 1, aResult("t1", "birds", core.StatusFail, "short"))
	withDetail.Detail = "[t1] the check\nat file.mjs:56"
	save(withDetail)
	toHistory(m)

	press(m, "enter") // run 2, which has detail
	hidden := m.View()
	mustContain(t, "run", hidden, "The test tool gave more detail", "Press d to show it", "d show the tool's detail")
	if strings.Contains(hidden, "Tool detail:") || strings.Contains(hidden, "file.mjs:56") {
		t.Errorf("the detail is shown before asking:\n%s", hidden)
	}

	press(m, "d")
	mustContain(t, "run", m.View(), "Tool detail:", "[t1] the check", "at file.mjs:56", "d hide the tool's detail")
	if strings.Contains(m.View(), "Press d to show it") {
		t.Errorf("the hint stays while the detail is shown:\n%s", m.View())
	}
	press(m, "d")
	if strings.Contains(m.View(), "file.mjs:56") {
		t.Errorf("d did not hide the detail again:\n%s", m.View())
	}

	press(m, "esc", "down", "enter") // run 1, which has none
	plain := m.View()
	press(m, "d")
	if m.View() != plain {
		t.Errorf("d changed a run with no detail:\n%s", m.View())
	}
	if strings.Contains(plain, "detail") {
		t.Errorf("a run with no detail mentions detail:\n%s", plain)
	}
}

func TestALongRunScrollsAndTheDetailKeyDoesNotPageDown(t *testing.T) {
	m, _, save := historyMenu(t, twoEnvironments)
	var results []core.Result
	for i := 1; i <= 40; i++ {
		results = append(results, aResult(fmt.Sprintf("check-%02d", i), "birds", core.StatusPass, ""))
	}
	save(aRun("local", 0, results...))
	toHistory(m)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	press(m, "enter")
	if strings.Contains(m.View(), "check-40") {
		t.Fatalf("the whole run fits in 12 lines?\n%s", m.View())
	}
	first := m.View()
	press(m, "d") // no detail: must not scroll
	if m.View() != first {
		t.Errorf("d scrolled the screen")
	}
	// The header is longer now (commit and tools lines), so page to the end.
	press(m, "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown")
	mustContain(t, "run", m.View(), "check-40")
	for i := 0; i < 60; i++ {
		press(m, "up")
	}
	mustContain(t, "run", m.View(), "Run 1  PASS")
}

// A test that printed several checks is one row; only its failed check is
// listed, and c shows every check and hides them again. A run where every test
// printed one check has nothing to show, so c does nothing and is not offered.
func TestCShowsEveryCheckOfATestThatPrintedSeveral(t *testing.T) {
	m, _, save := historyMenu(t, twoEnvironments)
	save(aRun("local", 0,
		aResult("t1", "birds", core.StatusPass, ""),
		aResult("t1", "birds", core.StatusFail, "expected 3, got 4"),
		aResult("t1", "birds", core.StatusPass, "")))
	toHistory(m)
	press(m, "enter")

	view := m.View()
	mustContain(t, "run", view, "t1  3 checks, 1 failed", "expected 3, got 4",
		"Some tests printed several checks; only their failed ones are listed. Press c to show every check.",
		"c show every check")
	// Two of the three checks have the same name here; the failing one is listed once.
	if n := strings.Count(view, "FAIL"); n < 2 {
		t.Errorf("the failed check is not listed under its test:\n%s", view)
	}

	press(m, "c")
	shown := m.View()
	if strings.Contains(shown, "Press c to show every check") {
		t.Errorf("the hint stays after c:\n%s", shown)
	}
	if n := strings.Count(shown, "the t1 check"); n != 3 {
		t.Errorf("every check should be listed after c; the name appears %d times:\n%s", n, shown)
	}
	mustContain(t, "run", shown, "c only the failed checks")

	press(m, "c")
	if strings.Contains(m.View(), "PASS  the t1 check") {
		t.Errorf("c did not hide the passing checks again:\n%s", m.View())
	}

	// A run where every test printed one check has nothing for c to do.
	m, _, save = historyMenu(t, twoEnvironments)
	save(aRun("local", 0, aResult("t1", "birds", core.StatusPass, ""), aResult("t2", "birds", core.StatusPass, "")))
	toHistory(m)
	press(m, "enter")
	plain := m.View()
	press(m, "c")
	if m.View() != plain {
		t.Errorf("c changed a run with nothing hidden")
	}
	if strings.Contains(plain, "c show every check") || strings.Contains(plain, "Press c") {
		t.Errorf("a run with nothing hidden offers c:\n%s", plain)
	}
}

func TestQAndCtrlCLeaveFromTheHistoryAndRunScreens(t *testing.T) {
	for _, quit := range []string{"q", "ctrl+c"} {
		m, _, save := historyMenu(t, twoEnvironments)
		save(aRun("local", 0, aResult("t1", "birds", core.StatusPass, "")))
		toHistory(m)
		if !quits(press(m, quit)) {
			t.Errorf("%s did not leave from the history", quit)
		}
		press(m, "enter")
		if _, ok := top(m).(*runScreen); !ok {
			t.Fatalf("top screen is %T; want a run", top(m))
		}
		if !quits(press(m, quit)) {
			t.Errorf("%s did not leave from a run", quit)
		}
	}
}

func TestHistoryScreensSurviveATinyTerminal(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 2}, {30, 5}, {200, 3}} {
		m, _, save := historyMenu(t, twoEnvironments)
		save(aRun("local", 0, aResult("t1", "birds", core.StatusFail, "a long message "+strings.Repeat("x", 80))))
		toHistory(m)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		press(m, "enter", "d", "down", "pgdown", "up")
		_ = m.View()
		press(m, "esc", "esc")
		_ = m.View()
	}
}

func TestAHistoryThatCannotBeOpenedSaysWhy(t *testing.T) {
	m, _, _ := historyMenu(t, twoEnvironments)
	m.deps.History = func(string) (core.Store, error) { return nil, errors.New("the history file is damaged") }
	// The screens made from here on take the dependencies of the chooser, so rebuild it.
	m.stack = []screen{newChooser(m.deps)}
	toHistory(m)
	mustContain(t, "history", m.View(), "the history file is damaged")
	press(m, "enter") // nothing to open
	if _, ok := top(m).(*historyScreen); !ok {
		t.Errorf("top screen is %T; want to stay on the history", top(m))
	}
}
