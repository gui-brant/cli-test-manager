package tui

import (
	"fmt"
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

const manifestFor = `
manifest_version: 1
project: %s
history: .tester/history.sqlite
features: [birds, security]
environments:
  deployed: {}
  local: {}
runners:
  node:
    run: ["node", "{files}"]
    format: tap
    output: stdout
    environments: [deployed, local]
    timeout: 1m
tests:
  - { id: one, name: One, feature: birds, runner: node, file: a.mjs }
  - { id: two, name: Two, feature: security, runner: node, file: b.mjs }
`

// project makes a folder with a valid manifest for a project of this name.
func project(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	text := strings.Replace(manifestFor, "%s", name, 1)
	if err := os.WriteFile(filepath.Join(dir, "test-manifest.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// menuWith opens the menu on a project list that already knows these projects,
// each given as name, nickname.
func menuWith(t *testing.T, known ...[2]string) (*Model, *projects.Service, []string) {
	t.Helper()
	svc := &projects.Service{File: filepath.Join(t.TempDir(), "tester", "projects.yaml")}
	var dirs []string
	for _, k := range known {
		dir := project(t, k[0])
		if _, _, err := svc.Add(dir, k[1], nil); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	m := New(Deps{Projects: svc, Load: manifestfile.Load, Loc: time.UTC})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, svc, dirs
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends keys one by one and returns the work the last one asked for.
func press(m *Model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = m.Update(key(k))
	}
	return cmd
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// mustContain checks the text as drawn, or, for a sentence the screen wrapped
// over two lines, the text with its line breaks and runs of spaces ignored.
func mustContain(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	flat := strings.Join(strings.Fields(text), " ")
	for _, w := range wants {
		if !strings.Contains(text, w) && !strings.Contains(flat, strings.Join(strings.Fields(w), " ")) {
			t.Errorf("%s does not contain %q:\n%s", what, w, text)
		}
	}
}

func TestTheChooserListsProjectsWithNicknamesAndMissingFolders(t *testing.T) {
	m, _, dirs := menuWith(t, [2]string{"se3316-lab3-birdwatching", "birdwatching"}, [2]string{"photo-app", ""})
	if err := os.RemoveAll(dirs[1]); err != nil {
		t.Fatal(err)
	}
	m.stack[0].(*chooser).reload("")
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 24}) // wide, so the long temporary paths fit

	v := m.View()
	mustContain(t, "view", v, "Projects", "birdwatching (se3316-lab3-birdwatching)", dirs[0], "photo-app", "(folder not found)",
		"enter open", "x forget", "r nickname", "q quit")

	// On an 80-column terminal the path is what gets cut, never the warning.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	narrow := m.View()
	if !strings.Contains(narrow, "(folder not found)") {
		t.Errorf("the warning was cut off on a narrow terminal:\n%s", narrow)
	}
	if !strings.Contains(v, "> birdwatching") {
		t.Errorf("the cursor should start on the first project:\n%s", v)
	}
}

func TestWithNoProjectsTheChooserSaysHowToGetOne(t *testing.T) {
	m, _, _ := menuWith(t)
	mustContain(t, "view", m.View(), "No projects yet", "tester run", "tester projects add")
	press(m, "enter", "x", "r") // nothing to open, forget or rename: nothing happens, no panic
	mustContain(t, "view", m.View(), "No projects yet")
}

func TestEnterOpensTheProjectAndEscGoesBackToTheSameRow(t *testing.T) {
	m, _, dirs := menuWith(t, [2]string{"one", ""}, [2]string{"se3316-lab3-birdwatching", "bw"})
	press(m, "down", "enter")
	if len(m.stack) != 2 {
		t.Fatalf("stack has %d screens; want 2", len(m.stack))
	}
	mustContain(t, "project screen", m.View(), "bw", "Project       se3316-lab3-birdwatching", "Folder        "+dirs[1],
		"Features      birds, security", "Environments  deployed, local", "Tests         2", "esc back")

	press(m, "esc")
	if len(m.stack) != 1 {
		t.Fatalf("stack has %d screens after Esc; want 1", len(m.stack))
	}
	if !strings.Contains(m.View(), "> bw") {
		t.Errorf("the cursor should still be on the project that was open:\n%s", m.View())
	}
}

func TestOpeningAMissingFolderStaysWithTheReason(t *testing.T) {
	m, _, dirs := menuWith(t, [2]string{"photo-app", ""})
	os.RemoveAll(dirs[0])
	m.stack[0].(*chooser).reload("")

	press(m, "enter")
	if len(m.stack) != 1 {
		t.Fatalf("a missing folder opened a screen")
	}
	mustContain(t, "view", m.View(), "the folder was not found", dirs[0], "Press x to forget it")
}

func TestOpeningAProjectWithABrokenManifestShowsTheError(t *testing.T) {
	m, _, dirs := menuWith(t, [2]string{"photo-app", ""})
	if err := os.WriteFile(filepath.Join(dirs[0], "test-manifest.yaml"), []byte("nonsense: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	press(m, "enter")
	if len(m.stack) != 1 {
		t.Fatal("a broken manifest opened a screen")
	}
	mustContain(t, "view", m.View(), "photo-app:")
}

func TestForgettingAsksFirstAndLeavesTheFolderAlone(t *testing.T) {
	m, svc, dirs := menuWith(t, [2]string{"one", ""}, [2]string{"two", ""})

	press(m, "x")
	mustContain(t, "question", m.View(), "Forget one?", "Its folder is not touched", "y forget")
	press(m, "n") // anything but y keeps it
	if got, _ := svc.List(); len(got) != 2 {
		t.Fatalf("a no still forgot: %+v", got)
	}
	if strings.Contains(m.View(), "Forget one?") {
		t.Error("the question stays after a no")
	}

	press(m, "x", "y")
	got, _ := svc.List()
	if len(got) != 1 || got[0].Name != "two" {
		t.Fatalf("List = %+v; want only two", got)
	}
	mustContain(t, "view", m.View(), "Forgot one.", "two")
	if strings.Contains(m.View(), "> one") {
		t.Errorf("the forgotten project is still listed:\n%s", m.View())
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "test-manifest.yaml")); err != nil {
		t.Errorf("forgetting touched the project's own files: %v", err)
	}
}

func TestRenamingFromTheChooser(t *testing.T) {
	m, svc, _ := menuWith(t, [2]string{"se3316-lab3-birdwatching", "old"})

	press(m, "r")
	mustContain(t, "prompt", m.View(), "Nickname for old", "empty clears", "enter save")
	press(m, "backspace", "backspace", "backspace", "birds", "enter")
	mustContain(t, "view", m.View(), "Renamed old to birds.", "birds (se3316-lab3-birdwatching)")

	press(m, "r", "esc") // cancelled
	if got, _ := svc.List(); got[0].Nickname != "birds" {
		t.Errorf("Esc changed the nickname: %+v", got)
	}

	press(m, "r", "backspace", "backspace", "backspace", "backspace", "backspace", "enter") // empty clears
	got, _ := svc.List()
	if got[0].Nickname != "" {
		t.Errorf("an empty nickname should clear it: %+v", got)
	}
	mustContain(t, "view", m.View(), "Cleared the nickname of se3316-lab3-birdwatching.")

	press(m, "r", "a/b", "enter") // not allowed: reported, nothing changes
	mustContain(t, "view", m.View(), "cannot be used")
	if got, _ := svc.List(); got[0].Nickname != "" {
		t.Errorf("an unusable nickname was stored: %+v", got)
	}
}

func TestLeavingTheMenu(t *testing.T) {
	m, _, _ := menuWith(t, [2]string{"one", ""})
	for _, k := range []string{"q", "esc", "ctrl+c"} {
		if !quits(press(m, k)) {
			t.Errorf("%s did not leave the menu from the chooser", k)
		}
	}
	press(m, "enter")
	if quits(press(m, "esc")) || len(m.stack) != 1 {
		t.Error("Esc on the project screen should go back, not leave")
	}
	press(m, "enter")
	if !quits(press(m, "q")) {
		t.Error("q on the project screen should leave the menu")
	}
	if !quits(press(m, "ctrl+c")) {
		t.Error("Ctrl-C should leave from any screen")
	}
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24}); cmd != nil {
		t.Error("a resize asked for work")
	}
}

func TestALongListScrollsAndTheCursorStaysOnScreen(t *testing.T) {
	var known [][2]string
	for i := 0; i < 30; i++ {
		known = append(known, [2]string{fmt.Sprintf("p%02d", i), ""})
	}
	m, _, _ := menuWith(t, known...)
	m.View() // sizes the list to the window

	for i := 0; i < 25; i++ {
		press(m, "down")
	}
	v := m.View()
	if !strings.Contains(v, "> p25") {
		t.Errorf("the cursor row p25 is not on screen:\n%s", v)
	}
	if strings.Contains(v, "p00") {
		t.Errorf("the top of the list is still shown after scrolling 25 rows:\n%s", v)
	}
	if lines := strings.Count(v, "\n") + 1; lines > 24 {
		t.Errorf("the view is %d lines; the window is 24", lines)
	}
	for i := 0; i < 20; i++ { // past the end: stays on the last project
		press(m, "down")
	}
	if !strings.Contains(m.View(), "> p29") {
		t.Errorf("the cursor should rest on the last project:\n%s", m.View())
	}
}

// A terminal that cannot say how big it is reports 0x0 (found on a pseudo-
// terminal): the screen keeps its size instead of shrinking to one row.
func TestAnUnknownTerminalSizeIsIgnored(t *testing.T) {
	m, _, _ := menuWith(t, [2]string{"one", ""}, [2]string{"two", ""}, [2]string{"three", ""})
	m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	v := m.View()
	mustContain(t, "view", v, "one", "two", "three")
}

func TestATinyTerminalDoesNotBreakTheScreens(t *testing.T) {
	m, _, _ := menuWith(t, [2]string{"se3316-lab3-birdwatching", "bw"}, [2]string{"photo-app", ""})
	for _, size := range [][2]int{{1, 1}, {5, 2}, {20, 3}, {10, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		press(m, "enter")
		_ = m.View()
		press(m, "esc", "x")
		_ = m.View()
		press(m, "n", "r")
		_ = m.View()
		press(m, "esc")
	}
}

// The menu as a real program: keys arrive one at a time through the framework,
// as from a person typing. Down, Enter to open the second project, then q.
func TestTheMenuRunsAsAProgram(t *testing.T) {
	m, _, _ := menuWith(t, [2]string{"one", ""}, [2]string{"two", ""})
	p := tea.NewProgram(m, tea.WithInput(&slowKeys{b: []byte("j\rq")}), tea.WithOutput(io.Discard))
	final, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	fm := final.(*Model)
	top, ok := fm.stack[len(fm.stack)-1].(*projectScreen)
	if !ok || top.entry.Name != "two" {
		t.Errorf("the program ended on %T; want the second project's screen", fm.stack[len(fm.stack)-1])
	}
}

// slowKeys feeds one key every few milliseconds, then ends.
type slowKeys struct{ b []byte }

func (r *slowKeys) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		time.Sleep(300 * time.Millisecond)
		return 0, io.EOF
	}
	time.Sleep(40 * time.Millisecond)
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}

func TestClipAndWrap(t *testing.T) {
	if got := clip("abcdef", 4); got != "abc…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("abc", 4); got != "abc" {
		t.Errorf("clip of a short line = %q", got)
	}
	if got := clip("abc", 1); got != "…" {
		t.Errorf("clip to one = %q", got)
	}
	if got := wrap("one two three four", 9); got != "one two\nthree\nfour" {
		t.Errorf("wrap = %q", got)
	}
	if got := wrap("abcdefghij", 4); got != "abcd\nefgh\nij" {
		t.Errorf("wrap of a long word = %q", got)
	}
	if got := wrap("keep\nlines", 80); got != "keep\nlines" {
		t.Errorf("wrap changed short lines: %q", got)
	}
}
