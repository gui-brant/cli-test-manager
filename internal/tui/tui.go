// Package tui is the interactive menu.
//
// It adds no logic of its own: it calls the same services as the plain commands
// and shows what they return. The framework (Bubble Tea and Bubbles) is
// imported here and nowhere else, so replacing it rewrites this package and
// nothing else.
//
// The menu is a stack of screens. The top screen gets every key; a screen
// returns where to go next (open another screen, go back, quit) and the menu
// does it. Esc goes back one screen, or leaves the menu from the first one.
package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

// Projects is the list of projects the chooser works on. The project list
// service is one.
type Projects interface {
	List() ([]projects.Entry, error)
	Remove(key string) (projects.Project, error)
	Rename(key, nickname string) (projects.Project, string, error)
}

// Deps is what the menu uses from the outside world.
type Deps struct {
	Projects Projects
	// Load reads a project's manifest from the path of its manifest file.
	Load func(path string) (*manifestfile.Loaded, error)
	// History opens the history file at this path for reading. It returns a nil
	// store and no error when there is no file yet, and never creates one.
	History func(path string) (core.Store, error)
	// Loc is the zone times are shown in. Nil means the local zone.
	Loc *time.Location

	// What running tests needs: the same parts the run command uses.
	Runner  core.Runner
	Creds   core.Credentials
	Environ func() []string
	Now     func() time.Time
	// Save opens the history file for writing, creating it when it is not there.
	Save func(path string) (core.Store, error)
	// Context ends a run in progress when the program is told to stop. Nil means
	// nothing does.
	Context context.Context
}

// nav says where a screen wants to go after a key.
type nav struct {
	push    screen // open this screen on top
	replace screen // put this screen where the current one is
	pop     bool   // go back one screen
	quit    bool   // leave the menu
}

// busyScreen is a screen that is doing something Ctrl-C must stop instead of
// leaving the menu: a run in progress.
type busyScreen interface{ busy() bool }

// screen is one page of the menu.
type screen interface {
	// update handles a message and returns the screen to keep on top, where to go
	// next, and any work for the framework.
	update(msg tea.Msg) (screen, nav, tea.Cmd)
	// view draws the screen in a space this wide and this high.
	view(width, height int) string
	// keys is the line that says which keys do what.
	keys() string
}

// Model is the menu.
type Model struct {
	deps          Deps
	stack         []screen
	width, height int
}

// New makes the menu, opened on the project chooser.
func New(d Deps) *Model {
	return &Model{deps: d, stack: []screen{newChooser(d)}, width: 80, height: 24}
}

// Init is part of the framework's interface; the menu starts with no work.
func (m *Model) Init() tea.Cmd { return nil }

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A terminal that does not know its size reports 0x0: keep what we have.
		if msg.Width > 0 && msg.Height > 0 {
			m.width, m.height = msg.Width, msg.Height
		}
		return m, nil
	case tea.KeyMsg:
		// Ctrl-C leaves from any screen, except one with a run in progress: that
		// screen cancels the run and stays.
		if b, ok := m.stack[len(m.stack)-1].(busyScreen); msg.Type == tea.KeyCtrlC && !(ok && b.busy()) {
			return m, tea.Quit
		}
	}

	top := len(m.stack) - 1
	next, n, cmd := m.stack[top].update(msg)
	m.stack[top] = next
	switch {
	case n.quit:
		return m, tea.Quit
	case n.replace != nil:
		m.stack[top] = n.replace
	case n.pop && len(m.stack) > 1:
		m.stack = m.stack[:top]
	case n.push != nil:
		m.stack = append(m.stack, n.push)
	}
	return m, cmd
}

// View draws the screen on top of the stack with its key line under it.
func (m *Model) View() string {
	top := m.stack[len(m.stack)-1]
	height := m.height - 1 // the key line
	if height < 1 {
		height = 1
	}
	return top.view(m.width, height) + "\n" + clip(top.keys(), m.width)
}

// Run opens the menu on the terminal and returns when the owner leaves it. A
// cancelled context (the program was told to stop) closes it and returns the
// context's error.
func Run(ctx context.Context, in io.Reader, out io.Writer, d Deps) error {
	// The styling library would otherwise ask the terminal for its background
	// colour and wait for the answer, which a terminal that does not support the
	// question (the Linux console, some multiplexers) never gives: the menu would
	// freeze at start. The menu draws no colours that depend on it.
	lipgloss.SetHasDarkBackground(true)

	d.Context = ctx
	// The program's own signal handling (ctx) stops the menu. The library's handler
	// would race it and could wait forever to hand over a message nobody reads
	// any more; Ctrl-C in the menu is a key, not a signal, so nothing is lost.
	p := tea.NewProgram(New(d), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithContext(ctx), tea.WithoutSignalHandler())
	if _, err := p.Run(); err != nil {
		if ctx.Err() != nil && (errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled)) {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// clip cuts s to at most width characters, with an ellipsis, so one line never
// wraps on a narrow terminal.
func clip(s string, width int) string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string([]rune(s)[:width-1]) + "…"
}

// wrap breaks s into lines of at most width characters, at spaces where it can.
func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		if utf8.RuneCountInString(para) <= width { // a line that fits keeps its spacing
			lines = append(lines, para)
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			for utf8.RuneCountInString(word) > width { // a word longer than a line
				if line != "" {
					lines = append(lines, line)
					line = ""
				}
				lines = append(lines, string([]rune(word)[:width]))
				word = string([]rune(word)[width:])
			}
			switch {
			case line == "":
				line = word
			case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width:
				line += " " + word
			default:
				lines = append(lines, line)
				line = word
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
