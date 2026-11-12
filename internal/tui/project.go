package tui

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

// selection is what the owner has chosen so far on the way to an action: the
// project, then a feature, then an environment (entries 37 and 38).
type selection struct {
	deps    Deps
	entry   projects.Entry
	loaded  *manifestfile.Loaded
	feature string
	env     string
}

func (s selection) manifest() *core.Manifest { return s.loaded.Manifest }

// breadcrumb is the path chosen so far, as a line at the top of each screen.
func (s selection) breadcrumb() string {
	parts := []string{s.entry.Display()}
	if s.feature != "" {
		parts = append(parts, s.feature)
	}
	if s.env != "" {
		parts = append(parts, s.env)
	}
	return strings.Join(parts, " › ")
}

// environments are the project's environment names, in order.
func (s selection) environments() []string {
	names := make([]string, 0, len(s.manifest().Environments))
	for name := range s.manifest().Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// testsOf counts the tests of a feature and, when env is not empty, how many of
// them can run in that environment (their runner allows it).
func (s selection) testsOf(feature, env string) (total, inEnv int) {
	m := s.manifest()
	for _, t := range m.Tests {
		if t.Feature != feature {
			continue
		}
		total++
		for _, e := range m.Runners[t.Runner].Environments {
			if env == "" || e == env {
				inEnv++
				break
			}
		}
	}
	return total, inEnv
}

func noun(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- a list of choices, shared by the three screens ----

type pickItem struct{ label, hint string }

type pickListItem struct{ pickItem }

func (p pickListItem) FilterValue() string { return p.label }

type pickDelegate struct{ labelWidth int }

func (pickDelegate) Height() int                               { return 1 }
func (pickDelegate) Spacing() int                              { return 0 }
func (pickDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d pickDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	p := it.(pickListItem)
	cursor := "  "
	if index == m.Index() {
		cursor = "> "
	}
	line := fmt.Sprintf("%s%-*s   %s", cursor, d.labelWidth, p.label, p.hint)
	fmt.Fprint(w, clip(line, m.Width()))
}

type picker struct {
	list  list.Model
	items []pickItem
}

func newPicker(items []pickItem) *picker {
	width := 0
	li := make([]list.Item, len(items))
	for i, it := range items {
		li[i] = pickListItem{it}
		width = max(width, len([]rune(it.label)))
	}
	l := list.New(li, pickDelegate{labelWidth: width}, 80, 10)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)
	return &picker{list: l, items: items}
}

// setLabel changes the label of choice i, keeping the cursor where it is.
func (p *picker) setLabel(i int, label string) {
	p.items[i].label = label
	p.list.SetItem(i, pickListItem{p.items[i]})
}

// at is the index of the choice under the cursor, or -1 when there are none.
func (p *picker) at() int {
	if len(p.items) == 0 {
		return -1
	}
	return p.list.Index()
}

func (p *picker) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return cmd
}

func (p *picker) view(width, rows int) string {
	if rows < 1 {
		rows = 1
	}
	p.list.SetSize(width, rows)
	return p.list.View()
}

// layout draws a screen's breadcrumb, its question, the choices and a status.
func layout(width, height int, crumb, question string, p *picker, status string) string {
	var b strings.Builder
	b.WriteString(clip(crumb, width))
	b.WriteString("\n")
	b.WriteString(clip(question, width))
	b.WriteString("\n\n")
	// The breadcrumb, the question, a blank line, a blank line and the status.
	b.WriteString(p.view(width, height-5))
	b.WriteString("\n\n")
	if status != "" {
		b.WriteString(wrap(status, width))
	}
	return b.String()
}

// layoutList is layout for a list that may have nothing in it: with no rows it
// shows the reason the list could not be read, or else the empty text.
func layoutList(width, height int, crumb, question string, p *picker, status, empty string) string {
	if len(p.items) > 0 {
		return layout(width, height, crumb, question, p, status)
	}
	text := empty
	if status != "" {
		text = status
	}
	return clip(crumb, width) + "\n" + clip(question, width) + "\n\n" + wrap(text, width)
}

// commonKeys handles the keys every choice screen shares. handled is false for
// any other key, which the caller then passes to the list.
func commonKeys(msg tea.Msg) (n nav, handled bool) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nav{}, false
	}
	switch key.String() {
	case "esc":
		return nav{pop: true}, true
	case "q":
		return nav{quit: true}, true
	}
	return nav{}, false
}

// ---- 2: features ----

// featureScreen lists the project's features. Choosing one goes on to the
// environments, or straight to the actions when the project has only one
// environment, since there is nothing to choose (entry 37).
type featureScreen struct {
	sel   selection
	names []string
	pick  *picker
}

func newFeatureScreen(d Deps, e projects.Entry, l *manifestfile.Loaded) *featureScreen {
	s := &featureScreen{sel: selection{deps: d, entry: e, loaded: l}}
	features := append([]string(nil), l.Manifest.Features...)
	// Tests that belong to no feature are in "general", which the manifest does
	// not have to list; without a row for it they could not be reached.
	if !slices.Contains(features, core.GeneralFeature) {
		if total, _ := s.sel.testsOf(core.GeneralFeature, ""); total > 0 {
			features = append(features, core.GeneralFeature)
		}
	}
	var items []pickItem
	for _, f := range features {
		total, _ := s.sel.testsOf(f, "")
		s.names = append(s.names, f)
		items = append(items, pickItem{label: f, hint: noun(total, "test", "tests")})
	}
	s.pick = newPicker(items)
	return s
}

func (s *featureScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		i := s.pick.at()
		if i < 0 {
			return s, nav{}, nil
		}
		next := s.sel
		next.feature = s.names[i]
		envs := next.environments()
		if len(envs) == 1 {
			next.env = envs[0]
			return s, nav{push: newActionScreen(next)}, nil
		}
		return s, nav{push: newEnvScreen(next)}, nil
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *featureScreen) view(width, height int) string {
	return layout(width, height, s.sel.breadcrumb(), "Choose a feature", s.pick, "")
}

func (s *featureScreen) keys() string { return "↑/↓ move · enter choose · esc back · q quit" }

// ---- 3: environments ----

type envScreen struct {
	sel   selection
	names []string
	pick  *picker
}

func newEnvScreen(sel selection) *envScreen {
	s := &envScreen{sel: sel, names: sel.environments()}
	total, _ := sel.testsOf(sel.feature, "")
	var items []pickItem
	for _, env := range s.names {
		_, inEnv := sel.testsOf(sel.feature, env)
		items = append(items, pickItem{label: env, hint: fmt.Sprintf("%d of %s can run here", inEnv, noun(total, "test", "tests"))})
	}
	s.pick = newPicker(items)
	return s
}

func (s *envScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		i := s.pick.at()
		if i < 0 {
			return s, nav{}, nil
		}
		next := s.sel
		next.env = s.names[i]
		return s, nav{push: newActionScreen(next)}, nil
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *envScreen) view(width, height int) string {
	return layout(width, height, s.sel.breadcrumb(), "Choose an environment", s.pick, "")
}

func (s *envScreen) keys() string { return "↑/↓ move · enter choose · esc back · q quit" }

// ---- 4: actions ----

// actionScreen lists what can be done with the chosen feature in the chosen
// environment. The actions themselves are built in later steps of the menu.
type actionScreen struct {
	sel    selection
	pick   *picker
	status string
}

func newActionScreen(sel selection) *actionScreen {
	total, inEnv := sel.testsOf(sel.feature, sel.env)
	items := []pickItem{
		{"Run history", fmt.Sprintf("the runs of %s in %s, newest first", sel.feature, sel.env)},
		{"Counts", "how each test has done"},
		{"Tests", fmt.Sprintf("the %s of %s, and their code", noun(total, "test", "tests"), sel.feature)}, {"Run all", fmt.Sprintf("%s in %s, asks first", noun(inEnv, "test", "tests"), sel.env)},
		{"Run several", "choose which of them to run"},
	}
	return &actionScreen{sel: sel, pick: newPicker(items)}
}

func (s *actionScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "enter" {
			i := s.pick.at()
			switch {
			case i < 0:
			case s.pick.items[i].label == "Run history":
				return s, nav{push: newHistoryScreen(s.sel)}, nil
			case s.pick.items[i].label == "Counts":
				return s, nav{push: newCountsScreen(s.sel)}, nil
			case s.pick.items[i].label == "Tests":
				return s, nav{push: newTestsScreen(s.sel)}, nil
			case s.pick.items[i].label == "Run all":
				next, err := newRunAllScreen(s.sel)
				if err != nil {
					s.status = err.Error()
					return s, nav{}, nil
				}
				return s, nav{push: next}, nil
			case s.pick.items[i].label == "Run several":
				next, err := newSeveralScreen(s.sel)
				if err != nil {
					s.status = err.Error()
					return s, nav{}, nil
				}
				return s, nav{push: next}, nil
			default:
				s.status = s.pick.items[i].label + " is built in a later step of the menu."
			}
			return s, nav{}, nil
		}
		s.status = ""
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *actionScreen) view(width, height int) string {
	return layout(width, height, s.sel.breadcrumb(), "Choose an action", s.pick, s.status)
}

func (s *actionScreen) keys() string { return "↑/↓ move · enter choose · esc back · q quit" }
