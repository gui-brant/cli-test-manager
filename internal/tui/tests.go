package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/query"
)

// testsScreen lists the tests of the chosen feature, numbered as in the manifest.
// Choosing one shows its code (entry 54).
type testsScreen struct {
	sel    selection
	rows   []query.TestRow
	pick   *picker
	status string
}

func newTestsScreen(sel selection) *testsScreen {
	s := &testsScreen{sel: sel}
	rows, err := query.Tests(sel.manifest(), sel.feature)
	if err != nil {
		s.status = err.Error()
	}
	s.rows = rows
	var items []pickItem
	for _, r := range rows {
		parts := []string{r.ID, r.Runner}
		if r.Tag != "" {
			parts = append(parts, r.Tag)
		}
		// The runner decides where a test can run (entry 38).
		if !slices.Contains(sel.manifest().Runners[r.Runner].Environments, sel.env) {
			parts = append(parts, "does not run in "+sel.env)
		}
		items = append(items, pickItem{label: fmt.Sprintf("#%d %s", r.Number, r.Name), hint: strings.Join(parts, " · ")})
	}
	s.pick = newPicker(items)
	return s
}

func (s *testsScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		if i := s.pick.at(); i >= 0 {
			return s, nav{push: newCodeScreen(s.sel, s.rows[i])}, nil
		}
		return s, nav{}, nil
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *testsScreen) view(width, height int) string {
	return layoutList(width, height, s.sel.breadcrumb(), fmt.Sprintf("The tests of %s, enter shows the code", s.sel.feature),
		s.pick, s.status, fmt.Sprintf("No tests belong to %s.", s.sel.feature))
}

func (s *testsScreen) keys() string {
	if len(s.pick.items) == 0 {
		return "esc back · q quit"
	}
	return "↑/↓ move · enter show the code · esc back · q quit"
}

// ---- one test's code ----

// codeScreen shows a test's file with line numbers. It scrolls.
type codeScreen struct {
	sel    selection
	row    query.TestRow
	text   string
	scroll scroller
}

func newCodeScreen(sel selection, row query.TestRow) *codeScreen {
	s := &codeScreen{sel: sel, row: row, scroll: newScroller()}
	head := fmt.Sprintf("#%d  %s  %s\nfeature: %s   runner: %s   tag: %s\nfile: %s\n\n",
		row.Number, row.ID, row.Name, row.Feature, row.Runner, orDash(row.Tag), row.File)
	code, err := query.TestCode(sel.loaded.Root, row)
	if err != nil {
		s.text = head + fmt.Sprintf("cannot read the test file %s: %v", row.File, err)
		return s
	}
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(code.Text, "\t", "    "), "\n"), "\n")
	digits := len(fmt.Sprint(len(lines)))
	var b strings.Builder
	b.WriteString(head)
	for i, l := range lines {
		fmt.Fprintf(&b, "%*d  %s\n", digits, i+1, l)
	}
	if code.Omitted > 0 {
		fmt.Fprintf(&b, "[... %d more bytes not shown ...]\n", code.Omitted)
	}
	s.text = strings.TrimSuffix(b.String(), "\n")
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (s *codeScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	return s, nav{}, s.scroll.update(msg)
}

func (s *codeScreen) view(width, height int) string {
	crumb := fmt.Sprintf("%s › test %d", s.sel.breadcrumb(), s.row.Number)
	return clip(crumb, width) + "\n\n" + s.scroll.render(s.text, width, height-2)
}

func (s *codeScreen) keys() string { return "↑/↓ scroll · pgup/pgdn page · esc back · q quit" }
