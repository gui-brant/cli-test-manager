package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

// passingRunner answers every command with one passing TAP result and remembers
// the arguments it was asked to run.
type passingRunner struct {
	mu    sync.Mutex
	files []string
}

func (r *passingRunner) Run(_ context.Context, inv core.Invocation) (core.Output, error) {
	r.mu.Lock()
	r.files = append(r.files, strings.Join(inv.Args, " "))
	r.mu.Unlock()
	return core.Output{Stdout: []byte("1..1\nok 1 - fine\n")}, nil
}

func (r *passingRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.files)
}

// blockingRunner never finishes a command until it is cancelled.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ core.Invocation) (core.Output, error) {
	<-ctx.Done()
	return core.Output{}, ctx.Err()
}

type noCreds struct{}

func (noCreds) Get(context.Context, string) (string, error) { return "", core.ErrNotFound }

// runMenu is historyMenu with what running tests needs.
func runMenu(t *testing.T, r core.Runner) (m *Model, historyFile string) {
	t.Helper()
	m, historyFile, _ = historyMenu(t, twoEnvironments)
	m.deps.Runner, m.deps.Creds = r, noCreds{}
	m.deps.Environ = func() []string { return nil }
	m.deps.Save = func(path string) (core.Store, error) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		return sqlite.Open(path)
	}
	m.stack = []screen{newChooser(m.deps)}
	return m, historyFile
}

// pump delivers what a command reports, and what the next commands report, to
// the menu until there are none: the run is over by then.
func pump(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

// toActions goes project › birds › an environment: local, or staging for 1.
func toActions(m *Model, envDown int) {
	press(m, "enter", "enter")
	for i := 0; i < envDown; i++ {
		press(m, "down")
	}
	press(m, "enter")
}

func storedRuns(t *testing.T, file string) int {
	t.Helper()
	s, err := sqlite.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.ListRuns(context.Background(), core.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

func TestRunAllAsksFirstWithTheRealCount(t *testing.T) {
	m, file := runMenu(t, &passingRunner{})
	toActions(m, 1) // staging, where t2 cannot run
	press(m, "down", "down", "down", "enter")
	if _, ok := top(m).(*confirmScreen); !ok {
		t.Fatalf("top screen is %T; want the question", top(m))
	}
	mustContain(t, "question", m.View(), "photo › birds › staging", "Run all",
		`Run 1 of 2 tests in environment "staging" (1 skipped)? (y/N)`, "skipped  t2", "y run · any other key goes back")

	press(m, "n") // anything but y
	if _, ok := top(m).(*actionScreen); !ok {
		t.Errorf("n went to %T; want back to the actions", top(m))
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("answering no created the history file")
	}
}

func TestRunAllRunsThenShowsTheSavedRun(t *testing.T) {
	r := &passingRunner{}
	m, file := runMenu(t, r)
	toActions(m, 0)
	press(m, "down", "down", "down", "enter")
	mustContain(t, "question", m.View(), `Run all 2 tests in environment "local"? (y/N)`)

	_, cmd := m.Update(key("y"))
	if _, ok := top(m).(*progressScreen); !ok {
		t.Fatalf("top screen is %T; want the progress", top(m))
	}
	pump(m, cmd)

	if r.calls() != 2 {
		t.Errorf("the runner was asked %d times; want 2", r.calls())
	}
	if _, ok := top(m).(*runScreen); !ok {
		t.Fatalf("top screen is %T; want the saved run", top(m))
	}
	mustContain(t, "run", m.View(), "photo › birds › local › run 1", "Run 1  PASS", "Results:")
	if n := storedRuns(t, file); n != 1 {
		t.Errorf("%d runs saved; want 1", n)
	}
	press(m, "esc")
	if _, ok := top(m).(*actionScreen); !ok {
		t.Errorf("Esc from the run went to %T; want the actions", top(m))
	}
}

func TestRunSeveralRunsOnlyTheTickedTests(t *testing.T) {
	r := &passingRunner{}
	m, _ := runMenu(t, r)
	toActions(m, 0)
	press(m, "down", "down", "down", "down", "enter")
	if _, ok := top(m).(*severalScreen); !ok {
		t.Fatalf("top screen is %T; want the choice of tests", top(m))
	}
	mustContain(t, "choice", m.View(), "Choose the tests of birds to run in local", "[ ] #1 One", "[ ] #2 Two", "space tick")

	press(m, "enter") // nothing ticked
	mustContain(t, "choice", m.View(), "Choose at least one test")
	press(m, "down", " ")
	mustContain(t, "choice", m.View(), "[ ] #1 One", "[x] #2 Two")
	press(m, "a")
	mustContain(t, "choice", m.View(), "[x] #1 One", "[x] #2 Two")
	press(m, "a")
	mustContain(t, "choice", m.View(), "[ ] #1 One", "[ ] #2 Two")
	press(m, " ") // Two again

	cmd := press(m, "enter")
	pump(m, cmd)
	if r.calls() != 1 || !strings.Contains(r.files[0], "b.mjs") {
		t.Errorf("the runner was asked for %v; want only b.mjs", r.files)
	}
	mustContain(t, "run", m.View(), "Run 1  PASS")
	press(m, "esc")
	mustContain(t, "choice", m.View(), "Choose the tests of birds", "[x] #2 Two") // the ticks are kept
}

func TestRunSeveralListsOnlyTestsThatCanRunInTheEnvironment(t *testing.T) {
	m, _ := runMenu(t, &passingRunner{})
	toActions(m, 1) // staging
	press(m, "down", "down", "down", "down", "enter")
	mustContain(t, "choice", m.View(), "#1 One")
	if strings.Contains(m.View(), "Two") {
		t.Errorf("a test that cannot run in staging is offered:\n%s", m.View())
	}
}

func TestCtrlCCancelsARunAndSavesNothing(t *testing.T) {
	m, file := runMenu(t, blockingRunner{})
	toActions(m, 0)
	press(m, "down", "down", "down", "enter")
	_, cmd := m.Update(key("y"))
	_, cmd = m.Update(cmd()) // the first test has started
	mustContain(t, "progress", m.View(), "Running 2 tests in local", "[1/2] t1 ... running", "esc or ctrl+c cancels")

	press(m, "q") // does nothing while running
	if _, ok := top(m).(*progressScreen); !ok {
		t.Fatalf("q left the running screen: %T", top(m))
	}
	if quits(press(m, "ctrl+c")) {
		t.Fatal("Ctrl-C left the menu instead of cancelling the run")
	}
	mustContain(t, "progress", m.View(), "Stopping the run")
	pump(m, cmd)

	mustContain(t, "progress", m.View(), "The run stopped", "The run was cancelled; nothing was saved.", "esc back")
	if n := storedRuns(t, file); n != 0 {
		t.Errorf("%d runs saved by a cancelled run", n)
	}
	if !quits(press(m, "q")) {
		t.Error("q did not leave once the run had stopped")
	}
}

func TestEscAlsoCancelsAndThenGoesBack(t *testing.T) {
	m, _ := runMenu(t, blockingRunner{})
	toActions(m, 0)
	press(m, "down", "down", "down", "enter")
	_, cmd := m.Update(key("y"))
	_, cmd = m.Update(cmd())
	press(m, "esc")
	pump(m, cmd)
	mustContain(t, "progress", m.View(), "The run was cancelled")
	press(m, "esc")
	if _, ok := top(m).(*actionScreen); !ok {
		t.Errorf("Esc went to %T; want the actions", top(m))
	}
}

func TestARunThatCannotBeSavedSaysSo(t *testing.T) {
	m, _ := runMenu(t, &passingRunner{})
	m.deps.Save = func(string) (core.Store, error) { return nil, errors.New("the disk is full") }
	m.stack = []screen{newChooser(m.deps)}
	toActions(m, 0)
	press(m, "down", "down", "down", "enter")
	_, cmd := m.Update(key("y"))
	pump(m, cmd)
	mustContain(t, "progress", m.View(), "The run stopped", "the disk is full")
}

func TestProgressScreensSurviveATinyTerminal(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 2}, {30, 5}, {200, 3}} {
		m, _ := runMenu(t, &passingRunner{})
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		toActions(m, 1)
		press(m, "down", "down", "down", "enter")
		_ = m.View()
		_, cmd := m.Update(key("y"))
		_ = m.View()
		pump(m, cmd)
		_ = m.View()
	}
}
