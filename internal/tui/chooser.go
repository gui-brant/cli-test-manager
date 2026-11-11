package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

// mode is what the chooser is doing besides browsing.
type mode int

const (
	browsing mode = iota
	forgetting
	renaming
)

// projectItem is one row of the chooser.
type projectItem struct{ projects.Entry }

func (p projectItem) FilterValue() string { return p.Display() }

// rowDelegate draws a project as one line.
type rowDelegate struct{}

func (rowDelegate) Height() int                               { return 1 }
func (rowDelegate) Spacing() int                              { return 0 }
func (rowDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (rowDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	e := it.(projectItem).Entry
	cursor := "  "
	if index == m.Index() {
		cursor = "> "
	}
	name := e.Display()
	if e.Nickname != "" && e.Name != "" {
		name += " (" + e.Name + ")"
	}
	// The marker comes before the path: on a narrow terminal it is the path that
	// gets cut off, never the warning.
	line := cursor + name
	if e.Missing {
		line += "   (folder not found)"
	}
	line += "   " + e.Path
	fmt.Fprint(w, clip(line, m.Width()))
}

// chooser is the first screen: the projects the program knows about.
type chooser struct {
	deps    Deps
	list    list.Model
	entries []projects.Entry
	status  string
	mode    mode
	input   textinput.Model
	width   int
	height  int
}

func newChooser(d Deps) *chooser {
	l := list.New(nil, rowDelegate{}, 80, 10)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)
	c := &chooser{deps: d, list: l, width: 80, height: 22}
	c.reload("")
	return c
}

// reload reads the list again and puts the cursor on the project whose folder is
// keep, or keeps it where it was when there is no such project.
func (c *chooser) reload(keep string) {
	at := c.list.Index()
	entries, err := c.deps.Projects.List()
	if err != nil {
		c.status = err.Error()
		entries = nil
	}
	c.entries = entries
	items := make([]list.Item, len(entries))
	for i, e := range entries {
		items[i] = projectItem{e}
	}
	c.list.SetItems(items)
	for i, e := range entries {
		if keep != "" && e.Path == keep {
			at = i
		}
	}
	if at >= len(entries) {
		at = len(entries) - 1
	}
	if at < 0 {
		at = 0
	}
	c.list.Select(at)
}

// current is the project under the cursor.
func (c *chooser) current() (projects.Entry, bool) {
	i := c.list.Index()
	if i < 0 || i >= len(c.entries) {
		return projects.Entry{}, false
	}
	return c.entries[i], true
}

func (c *chooser) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		var cmd tea.Cmd
		c.list, cmd = c.list.Update(msg)
		return c, nav{}, cmd
	}

	switch c.mode {
	case forgetting:
		return c.answerForget(key)
	case renaming:
		return c.typeNickname(key)
	}

	switch key.String() {
	case "q", "esc":
		return c, nav{quit: true}, nil
	case "enter":
		return c.open()
	case "x":
		if e, ok := c.current(); ok {
			c.mode = forgetting
			c.status = fmt.Sprintf("Forget %s? Its folder is not touched. (y/N)", e.Display())
		}
		return c, nav{}, nil
	case "r":
		if e, ok := c.current(); ok {
			c.mode = renaming
			c.input = textinput.New()
			c.input.Prompt = fmt.Sprintf("Nickname for %s (empty clears, Esc cancels): ", e.Display())
			c.input.SetValue(e.Nickname)
			c.input.Focus()
			c.status = ""
		}
		return c, nav{}, nil
	}
	c.status = ""
	var cmd tea.Cmd
	c.list, cmd = c.list.Update(msg)
	return c, nav{}, cmd
}

// open shows the project under the cursor. A project whose folder is gone, or
// whose manifest is not valid, stays on this screen with the reason.
func (c *chooser) open() (screen, nav, tea.Cmd) {
	e, ok := c.current()
	if !ok {
		return c, nav{}, nil
	}
	if e.Missing {
		c.status = fmt.Sprintf("%s: the folder was not found (%s). Press x to forget it.", e.Display(), e.Path)
		return c, nav{}, nil
	}
	loaded, err := c.deps.Load(filepath.Join(e.Path, manifestfile.FileName))
	if err != nil {
		c.status = fmt.Sprintf("%s: %v", e.Display(), err)
		return c, nav{}, nil
	}
	c.status = ""
	return c, nav{push: newFeatureScreen(c.deps, e, loaded)}, nil
}

func (c *chooser) answerForget(key tea.KeyMsg) (screen, nav, tea.Cmd) {
	c.mode = browsing
	e, ok := c.current()
	if !ok || key.String() != "y" {
		c.status = ""
		return c, nav{}, nil
	}
	if _, err := c.deps.Projects.Remove(e.Path); err != nil {
		c.status = err.Error()
		return c, nav{}, nil
	}
	c.reload("")
	c.status = fmt.Sprintf("Forgot %s.", e.Display())
	return c, nav{}, nil
}

func (c *chooser) typeNickname(key tea.KeyMsg) (screen, nav, tea.Cmd) {
	switch key.String() {
	case "esc":
		c.mode = browsing
		c.status = ""
		return c, nav{}, nil
	case "enter":
		c.mode = browsing
		e, ok := c.current()
		if !ok {
			return c, nav{}, nil
		}
		p, previous, err := c.deps.Projects.Rename(e.Path, c.input.Value())
		if err != nil {
			c.status = strings.TrimPrefix(err.Error(), "projects: ")
			return c, nav{}, nil
		}
		c.reload(e.Path)
		if p.Nickname == "" {
			c.status = fmt.Sprintf("Cleared the nickname of %s.", p.Display())
		} else {
			c.status = fmt.Sprintf("Renamed %s to %s.", previous, p.Display())
		}
		return c, nav{}, nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(key)
	return c, nav{}, cmd
}

func (c *chooser) view(width, height int) string {
	c.width, c.height = width, height
	var b strings.Builder
	b.WriteString("Projects\n\n")

	// The title, a blank line, a blank line and the status take four lines.
	rows := height - 4
	if rows < 1 {
		rows = 1
	}
	if len(c.entries) == 0 {
		b.WriteString(wrap("No projects yet. A project is added when you use `tester run` or `tester history` in it, or with `tester projects add`.", width))
		b.WriteString("\n")
	} else {
		c.list.SetSize(width, rows)
		b.WriteString(c.list.View())
	}

	b.WriteString("\n\n")
	switch {
	case c.mode == renaming:
		b.WriteString(clip(c.input.View(), width))
	case c.status != "":
		b.WriteString(wrap(c.status, width))
	}
	return b.String()
}

func (c *chooser) keys() string {
	switch c.mode {
	case forgetting:
		return "y forget · any other key keeps it"
	case renaming:
		return "enter save · esc cancel"
	}
	return "↑/↓ move · enter open · r nickname · x forget · q quit"
}
