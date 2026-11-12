package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/query"
	"github.com/gui-brant/cli-test-manager/internal/view"
)

// countsScreen shows how each test of the chosen feature has done in the chosen
// environment, as `tester counts --feature --env` does (entry 54).
type countsScreen struct {
	sel    selection
	pick   *picker
	status string
}

func newCountsScreen(sel selection) *countsScreen {
	s := &countsScreen{sel: sel}
	var rows []query.CountRow
	err := sel.withHistory(func(q *query.Service) error {
		if q == nil {
			return nil
		}
		var err error
		rows, err = q.Counts(context.Background(), sel.manifest(), core.StatsFilter{Feature: sel.feature, Environment: sel.env})
		return err
	})
	if err != nil {
		s.status = err.Error()
		rows = nil
	}
	var items []pickItem
	for _, r := range rows {
		num := "-"
		if r.Number > 0 {
			num = fmt.Sprintf("#%d", r.Number)
		}
		name := r.Name
		if name == "" {
			name = r.TestID
		}
		parts := []string{noun(r.Runs, "run", "runs"), fmt.Sprintf("%d passed", r.Passed), fmt.Sprintf("%d failed", r.Failed)}
		if r.Errored > 0 {
			parts = append(parts, fmt.Sprintf("%d errored", r.Errored))
		}
		if r.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped", r.Skipped))
		}
		parts = append(parts, fmt.Sprintf("last %s %s", strings.ToUpper(string(r.LastStatus)), view.When(sel.deps.Loc, r.LastRunAt)))
		items = append(items, pickItem{label: num + " " + name, hint: strings.Join(parts, " · ")})
	}
	s.pick = newPicker(items)
	return s
}

// refresh reads the counts again, after runs were pulled, keeping the cursor.
func (s *countsScreen) refresh() {
	at := s.pick.at()
	fresh := newCountsScreen(s.sel)
	s.pick, s.status = fresh.pick, fresh.status
	if at > 0 && len(s.pick.items) > 0 {
		s.pick.list.Select(min(at, len(s.pick.items)-1))
	}
}

func (s *countsScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *countsScreen) view(width, height int) string {
	return layoutList(width, height, s.sel.breadcrumb(), fmt.Sprintf("How the tests of %s have done in %s", s.sel.feature, s.sel.env),
		s.pick, s.status, fmt.Sprintf("No results of %s in %s yet.", s.sel.feature, s.sel.env))
}

func (s *countsScreen) keys() string {
	if len(s.pick.items) == 0 {
		return "esc back · q quit"
	}
	return "↑/↓ move · esc back · q quit"
}
