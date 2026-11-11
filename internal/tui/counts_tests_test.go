package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// toCounts goes project › birds › local › Counts; toTests goes to Tests instead.
func toCounts(m *Model) { press(m, "enter", "enter", "enter", "down", "enter") }
func toTests(m *Model)  { press(m, "enter", "enter", "enter", "down", "down", "enter") }

// projectFolder is the folder of the project historyMenu made, from its history file.
func projectFolder(historyFile string) string { return filepath.Dir(filepath.Dir(historyFile)) }

func TestCountsListEachTestOfTheFeatureInTheEnvironment(t *testing.T) {
	m, _, save := historyMenu(t, twoEnvironments)
	save(aRun("local", 0, aResult("t1", "birds", core.StatusPass, ""), aResult("t2", "birds", core.StatusFail, "")))
	save(aRun("local", 1, aResult("t1", "birds", core.StatusFail, "")))
	save(aRun("staging", 2, aResult("t1", "birds", core.StatusPass, "")))  // another environment
	save(aRun("local", 3, aResult("t3", "security", core.StatusPass, ""))) // another feature
	toCounts(m)
	if _, ok := top(m).(*countsScreen); !ok {
		t.Fatalf("top screen is %T; want the counts", top(m))
	}
	mustContain(t, "counts", m.View(), "photo › birds › local", "How the tests of birds have done in local",
		"#1 One", "2 runs · 1 passed · 1 failed · last FAIL 2026-11-11 15:10:32",
		"#2 Two", "1 run · 0 passed · 1 failed · last FAIL 2026-11-11 15:09:32")
	if strings.Contains(m.View(), "Three") {
		t.Errorf("a test of another feature is listed:\n%s", m.View())
	}
	press(m, "enter") // nothing to open
	if _, ok := top(m).(*countsScreen); !ok {
		t.Errorf("Enter left the counts: %T", top(m))
	}
	press(m, "esc")
	if _, ok := top(m).(*actionScreen); !ok {
		t.Errorf("Esc went to %T; want the actions", top(m))
	}
}

func TestCountsWithNoHistorySaySoAndCreateNothing(t *testing.T) {
	m, file, _ := historyMenu(t, twoEnvironments)
	toCounts(m)
	mustContain(t, "counts", m.View(), "No results of birds in local yet.")
	if _, err := os.Stat(file); err == nil {
		t.Error("looking at the counts created the history file")
	}
}

func TestTestsAreListedWithTheirNumbersAndWhereTheyCannotRun(t *testing.T) {
	m, _, _ := historyMenu(t, twoEnvironments)
	press(m, "enter", "enter", "down", "enter", "down", "down", "enter") // birds › staging › Tests
	if _, ok := top(m).(*testsScreen); !ok {
		t.Fatalf("top screen is %T; want the tests", top(m))
	}
	view := m.View()
	mustContain(t, "tests", view, "photo › birds › staging", "The tests of birds", "#1 One", "t1 · both", "#2 Two", "t2 · localonly · does not run in staging")
	if strings.Contains(view, "Three") {
		t.Errorf("a test of another feature is listed:\n%s", view)
	}
	if strings.Count(view, "does not run") != 1 {
		t.Errorf("only the local-only test should say it does not run in staging:\n%s", view)
	}
}

func TestChoosingATestShowsItsCodeWithLineNumbers(t *testing.T) {
	m, file, _ := historyMenu(t, twoEnvironments)
	var code strings.Builder
	for i := 1; i <= 12; i++ {
		code.WriteString("\tconsole.log('line " + strings.Repeat("x", i%3) + "')\n")
	}
	if err := os.WriteFile(filepath.Join(projectFolder(file), "a.mjs"), []byte(code.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	toTests(m)
	press(m, "enter") // #1
	if _, ok := top(m).(*codeScreen); !ok {
		t.Fatalf("top screen is %T; want the code", top(m))
	}
	view := m.View()
	mustContain(t, "code", view, "photo › birds › local › test 1", "#1  t1  One", "feature: birds   runner: both   tag: -", "file: a.mjs",
		" 1      console.log('line x')", "12      console.log('line ')")
	if strings.Contains(view, "\t") {
		t.Errorf("a tab was left in the code:\n%q", view)
	}
	press(m, "esc")
	mustContain(t, "tests", m.View(), "The tests of birds", "> #1 One")
}

func TestCodeThatCannotBeReadSaysSo(t *testing.T) {
	m, _, _ := historyMenu(t, twoEnvironments) // b.mjs does not exist
	toTests(m)
	press(m, "down", "enter")
	mustContain(t, "code", m.View(), "#2  t2  Two", "cannot read the test file b.mjs")
}

func TestALongFileScrolls(t *testing.T) {
	m, file, _ := historyMenu(t, twoEnvironments)
	var code strings.Builder
	for i := 1; i <= 60; i++ {
		code.WriteString("statement number\n")
	}
	if err := os.WriteFile(filepath.Join(projectFolder(file), "a.mjs"), []byte(code.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	toTests(m)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	press(m, "enter")
	if strings.Contains(m.View(), "60  statement") {
		t.Fatalf("all 60 lines fit in 12?\n%s", m.View())
	}
	_ = m.View() // the program draws after every key, which is what sizes the scrolling area
	for i := 0; i < 8; i++ {
		press(m, "pgdown")
		_ = m.View()
	}
	mustContain(t, "code", m.View(), "60  statement number")
}

func TestQAndCtrlCLeaveFromCountsTestsAndCode(t *testing.T) {
	for _, quit := range []string{"q", "ctrl+c"} {
		for name, goTo := range map[string]func(*Model){"counts": toCounts, "tests": toTests} {
			m, _, _ := historyMenu(t, twoEnvironments)
			goTo(m)
			if !quits(press(m, quit)) {
				t.Errorf("%s did not leave from the %s", quit, name)
			}
		}
		m, _, _ := historyMenu(t, twoEnvironments)
		toTests(m)
		press(m, "enter")
		if !quits(press(m, quit)) {
			t.Errorf("%s did not leave from the code", quit)
		}
	}
}

func TestCountsTestsAndCodeSurviveATinyTerminal(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 2}, {30, 5}, {200, 3}} {
		m, file, save := historyMenu(t, twoEnvironments)
		save(aRun("local", 0, aResult("t1", "birds", core.StatusFail, "")))
		if err := os.WriteFile(filepath.Join(projectFolder(file), "a.mjs"), []byte("one\ntwo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		toCounts(m)
		_ = m.View()
		press(m, "esc", "down", "enter") // actions › Tests
		_ = m.View()
		press(m, "enter", "down", "pgdown", "up")
		_ = m.View()
		press(m, "esc", "esc", "esc")
		_ = m.View()
	}
}
