package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

// projectScreen is what opens when a project is chosen. For now it shows what
// the project's manifest declares; the feature, environment and action screens
// that follow are the next step of the menu.
type projectScreen struct {
	entry  projects.Entry
	loaded *manifestfile.Loaded
}

func newProjectScreen(e projects.Entry, l *manifestfile.Loaded) *projectScreen {
	return &projectScreen{entry: e, loaded: l}
}

func (p *projectScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return p, nav{pop: true}, nil
		case "q":
			return p, nav{quit: true}, nil
		}
	}
	return p, nav{}, nil
}

func (p *projectScreen) view(width, height int) string {
	m := p.loaded.Manifest
	envs := make([]string, 0, len(m.Environments))
	for name := range m.Environments {
		envs = append(envs, name)
	}
	sort.Strings(envs)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", p.entry.Display())
	fmt.Fprintf(&b, "Project       %s\n", m.Project)
	fmt.Fprintf(&b, "Folder        %s\n", p.entry.Path)
	fmt.Fprintf(&b, "Features      %s\n", strings.Join(m.Features, ", "))
	fmt.Fprintf(&b, "Environments  %s\n", strings.Join(envs, ", "))
	fmt.Fprintf(&b, "Tests         %d\n\n", len(m.Tests))
	b.WriteString("The screens for features, environments and actions come next.")
	return wrap(b.String(), width)
}

func (p *projectScreen) keys() string { return "esc back · q quit" }
