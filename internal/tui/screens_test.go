package tui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

// A project like lab3: one environment, so there is nothing to choose.
const oneEnvironment = `
manifest_version: 1
project: lab3
history: .tester/history.sqlite
features: [birds, security]
environments:
  deployed: {}
runners:
  node:
    run: ["node", "{files}"]
    format: tap
    output: stdout
    environments: [deployed]
    timeout: 1m
tests:
  - { id: birds-search, name: Birds search, feature: birds, runner: node, file: a.mjs }
  - { id: hostile, name: Hostile input, feature: security, runner: node, file: b.mjs }
`

// A project with two environments, where one of the two bird tests only runs in
// "local" (its runner allows nothing else).
const twoEnvironments = `
manifest_version: 1
project: photo
history: .tester/history.sqlite
features: [birds, security]
environments:
  local: {}
  staging: {}
runners:
  both:
    run: ["node", "{files}"]
    format: tap
    output: stdout
    environments: [local, staging]
    timeout: 1m
  localonly:
    run: ["node", "{files}"]
    format: tap
    output: stdout
    environments: [local]
    timeout: 1m
tests:
  - { id: t1, name: One, feature: birds, runner: both, file: a.mjs }
  - { id: t2, name: Two, feature: birds, runner: localonly, file: b.mjs }
  - { id: t3, name: Three, feature: security, runner: both, file: c.mjs }
`

// menuFor opens the menu on projects with these manifests.
func menuFor(t *testing.T, manifests ...string) *Model {
	t.Helper()
	svc := &projects.Service{File: filepath.Join(t.TempDir(), "tester", "projects.yaml")}
	for _, text := range manifests {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "test-manifest.yaml"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.Add(dir, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	m := New(Deps{Projects: svc, Load: manifestfile.Load, Loc: time.UTC})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

func top(m *Model) screen { return m.stack[len(m.stack)-1] }

func TestFeaturesAreListedWithTheirTestCounts(t *testing.T) {
	m := menuFor(t, twoEnvironments)
	press(m, "enter")
	if _, ok := top(m).(*featureScreen); !ok {
		t.Fatalf("top screen is %T; want the features", top(m))
	}
	mustContain(t, "features", m.View(), "photo", "Choose a feature", "birds", "2 tests", "security", "1 test", "enter choose")
	if strings.Contains(m.View(), "general") {
		t.Errorf("a feature with no tests was listed:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "> birds") {
		t.Errorf("the cursor should start on the first feature:\n%s", m.View())
	}
}

// Tests that belong to "general" are reachable even though the manifest does not
// list that feature.
func TestTheGeneralFeatureAppearsWhenSomeTestsBelongToIt(t *testing.T) {
	withGeneral := strings.Replace(oneEnvironment, "  - { id: hostile",
		"  - { id: misc, name: Misc, feature: general, runner: node, file: c.mjs }\n  - { id: hostile", 1)
	m := menuFor(t, withGeneral)
	press(m, "enter")
	mustContain(t, "features", m.View(), "birds", "security", "general", "1 test")
	press(m, "down", "down", "enter") // general
	mustContain(t, "actions", m.View(), "lab3 › general › deployed", "1 test in deployed")
}

func TestAProjectWithTwoEnvironmentsAsksWhichOne(t *testing.T) {
	m := menuFor(t, twoEnvironments)
	press(m, "enter", "enter") // the project, then the feature "birds"
	if _, ok := top(m).(*envScreen); !ok {
		t.Fatalf("top screen is %T; want the environments", top(m))
	}
	mustContain(t, "environments", m.View(), "photo › birds", "Choose an environment",
		"local", "2 of 2 tests can run here", "staging", "1 of 2 tests can run here")

	press(m, "down", "enter") // staging
	if _, ok := top(m).(*actionScreen); !ok {
		t.Fatalf("top screen is %T; want the actions", top(m))
	}
	mustContain(t, "actions", m.View(), "photo › birds › staging", "Choose an action",
		"Run history", "Counts", "Tests", "Run all", "1 test in staging, asks first", "Run several")
}

// Entry 37: with one environment there is nothing to choose, so the step is skipped.
func TestAProjectWithOneEnvironmentSkipsThatQuestion(t *testing.T) {
	m := menuFor(t, oneEnvironment)
	press(m, "enter", "enter")
	if _, ok := top(m).(*actionScreen); !ok {
		t.Fatalf("top screen is %T; want the actions straight away", top(m))
	}
	if len(m.stack) != 3 {
		t.Errorf("stack has %d screens; want projects, features, actions", len(m.stack))
	}
	mustContain(t, "actions", m.View(), "lab3 › birds › deployed", "Choose an action", "1 test in deployed")

	press(m, "esc") // back goes to the features, not to a skipped screen
	if _, ok := top(m).(*featureScreen); !ok {
		t.Errorf("Esc went to %T; want the features", top(m))
	}
}

func TestEscStepsBackOneScreenAtATimeKeepingTheCursor(t *testing.T) {
	m := menuFor(t, twoEnvironments)
	press(m, "enter", "down", "enter", "down", "enter") // photo, security, staging
	if _, ok := top(m).(*actionScreen); !ok {
		t.Fatalf("top screen is %T", top(m))
	}
	mustContain(t, "actions", m.View(), "photo › security › staging")

	press(m, "esc")
	mustContain(t, "environments", m.View(), "Choose an environment", "> staging")
	press(m, "esc")
	mustContain(t, "features", m.View(), "Choose a feature", "> security")
	press(m, "esc")
	mustContain(t, "projects", m.View(), "Projects", "> photo")
	if len(m.stack) != 1 {
		t.Errorf("stack has %d screens; want the chooser alone", len(m.stack))
	}
}

func TestActionsAreBuiltLaterAndSayWhenChosen(t *testing.T) {
	m := menuFor(t, oneEnvironment)
	press(m, "enter", "enter")
	press(m, "down", "enter") // Run history is built; Counts is not yet
	mustContain(t, "status", m.View(), "Counts is built in a later step of the menu")
	press(m, "down", "down", "enter")
	mustContain(t, "status", m.View(), "Run all is built in a later step of the menu", "> Run all")
	press(m, "down")
	if strings.Contains(m.View(), "is built in a later step") {
		t.Errorf("the note stays after moving:\n%s", m.View())
	}
	if _, ok := top(m).(*actionScreen); !ok {
		t.Errorf("choosing an action left the screen: %T", top(m))
	}
}

// q and Ctrl-C leave from every screen, one level deeper each time.
func TestEveryScreenLeavesOnQAndCtrlC(t *testing.T) {
	for _, quit := range []string{"q", "ctrl+c"} {
		m := menuFor(t, twoEnvironments)
		for depth := 1; depth <= 4; depth++ {
			if !quits(press(m, quit)) {
				t.Errorf("%s did not leave the menu at depth %d (%T)", quit, depth, top(m))
			}
			press(m, "enter") // one screen deeper; the last press does nothing new
		}
	}
}

func TestEveryScreenSurvivesATinyTerminal(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 2}, {30, 5}, {200, 3}} {
		m := menuFor(t, twoEnvironments, oneEnvironment)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		press(m, "enter", "enter", "enter") // first project, feature, environment
		_ = m.View()
		press(m, "enter", "down", "down") // an action, then moving
		_ = m.View()
		press(m, "esc", "esc", "esc", "down", "enter", "enter") // back to projects, then the second project through to its actions
		_ = m.View()
	}
}

// The whole way through as a real program: project, feature, environment, then q.
func TestTheMenuWalksThroughAllScreensAsAProgram(t *testing.T) {
	m := menuFor(t, twoEnvironments)
	p := tea.NewProgram(m, tea.WithInput(&slowKeys{b: []byte("\r\r\rq")}), tea.WithOutput(io.Discard))
	final, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	fm := final.(*Model)
	act, ok := fm.stack[len(fm.stack)-1].(*actionScreen)
	if !ok || act.sel.entry.Name != "photo" || act.sel.feature != "birds" || act.sel.env != "local" {
		t.Errorf("the program ended on %T; want the actions of photo › birds › local", fm.stack[len(fm.stack)-1])
	}
}
