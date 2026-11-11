package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	bkey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/query"
	"github.com/gui-brant/cli-test-manager/internal/view"
)

// withHistory runs f on the project's history, opened for the call and closed
// after it. A project with no history file yet gives f a nil service, so a
// screen that only reads never creates the file.
func (s selection) withHistory(f func(q *query.Service) error) error {
	if s.deps.History == nil {
		return errors.New("this menu was started without a way to read history")
	}
	store, err := s.deps.History(s.loaded.HistoryPath)
	if err != nil {
		return err
	}
	if store == nil {
		return f(nil)
	}
	defer store.Close()
	return f(&query.Service{Store: store})
}

// ---- run history: the runs of the chosen feature in the chosen environment ----

// historyScreen lists the runs that have a result in the chosen feature and ran
// against the chosen environment, newest first (entry 53).
type historyScreen struct {
	sel    selection
	runs   []core.RunSummary
	pick   *picker
	status string // why the list could not be read, or a note about it
}

func newHistoryScreen(sel selection) *historyScreen {
	s := &historyScreen{sel: sel}
	err := sel.withHistory(func(q *query.Service) error {
		if q == nil {
			return nil
		}
		runs, err := q.History(context.Background(), core.RunFilter{Feature: sel.feature, Environment: sel.env})
		s.runs = runs
		return err
	})
	if err != nil {
		s.status = err.Error()
		s.runs = nil
	}
	var items []pickItem
	for _, r := range s.runs {
		hint := fmt.Sprintf("%s  %-4s  %d/%d failed", view.When(sel.deps.Loc, r.StartedAt),
			view.ResultWord(r.State, r.Outcome), r.Failed, r.Total)
		// Runs the tester started are the usual case; only a pulled one is marked.
		if r.Source == core.SourceRemote {
			hint += "  (remote)"
		}
		if r.Reason != "" {
			hint += "  " + r.Reason
		}
		items = append(items, pickItem{label: fmt.Sprintf("Run %d", r.ID), hint: hint})
	}
	s.pick = newPicker(items)
	if err == nil && len(s.runs) >= core.DefaultListLimit {
		s.status = fmt.Sprintf("Showing the newest %d runs.", core.DefaultListLimit)
	}
	return s
}

func (s *historyScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		i := s.pick.at()
		if i < 0 {
			return s, nav{}, nil
		}
		next, err := newRunScreen(s.sel, s.runs[i].ID)
		if err != nil {
			s.status = err.Error()
			return s, nav{}, nil
		}
		return s, nav{push: next}, nil
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *historyScreen) view(width, height int) string {
	question := fmt.Sprintf("Runs of %s in %s, newest first", s.sel.feature, s.sel.env)
	if len(s.runs) == 0 {
		var b strings.Builder
		b.WriteString(clip(s.sel.breadcrumb(), width))
		b.WriteString("\n")
		b.WriteString(clip(question, width))
		b.WriteString("\n\n")
		if s.status == "" {
			b.WriteString(wrap(fmt.Sprintf("No runs of %s in %s yet.", s.sel.feature, s.sel.env), width))
		} else {
			b.WriteString(wrap(s.status, width))
		}
		return b.String()
	}
	return layout(width, height, s.sel.breadcrumb(), question, s.pick, s.status)
}

func (s *historyScreen) keys() string {
	if len(s.runs) == 0 {
		return "esc back · q quit"
	}
	return "↑/↓ move · enter open the run · esc back · q quit"
}

// ---- one run ----

// runScreen shows one run as `tester history <n>` does: its header, its
// results, the diagnostic when it has one, and the test tool's own detail on a
// key (entry 42). It scrolls.
type runScreen struct {
	sel        selection
	id         int64
	parts      view.Run
	showDetail bool
	vp         viewport.Model
}

func newRunScreen(sel selection, id int64) (*runScreen, error) {
	var parts view.Run
	err := sel.withHistory(func(q *query.Service) error {
		if q == nil {
			return fmt.Errorf("run %d not found: there is no history yet", id)
		}
		d, err := q.Run(context.Background(), sel.manifest(), id)
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("run %d not found", id)
		}
		if err != nil {
			return err
		}
		parts = view.Build(d, id, sel.deps.Loc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	vp := viewport.New(80, 20)
	// The viewport pages with d and u; here d is the detail key.
	vp.KeyMap.HalfPageDown = bkey.NewBinding(bkey.WithKeys("ctrl+d"))
	vp.KeyMap.HalfPageUp = bkey.NewBinding(bkey.WithKeys("ctrl+u"))
	vp.KeyMap.Left = bkey.NewBinding()
	vp.KeyMap.Right = bkey.NewBinding()
	return &runScreen{sel: sel, id: id, parts: parts, vp: vp}, nil
}

// text is everything the screen shows, in the order of the command.
func (s *runScreen) text() string {
	chunks := []string{s.parts.Header, s.parts.Results}
	if s.parts.Diagnostic != "" {
		chunks = append(chunks, s.parts.Diagnostic)
	}
	if s.parts.HasDetail() {
		if s.showDetail {
			chunks = append(chunks, s.parts.Detail)
		} else {
			chunks = append(chunks, view.DetailHint+" Press d to show it.")
		}
	}
	return strings.Join(chunks, "\n\n")
}

func (s *runScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "d" {
		if s.parts.HasDetail() {
			s.showDetail = !s.showDetail
		}
		return s, nav{}, nil
	}
	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, nav{}, cmd
}

func (s *runScreen) view(width, height int) string {
	var b strings.Builder
	b.WriteString(clip(fmt.Sprintf("%s › run %d", s.sel.breadcrumb(), s.id), width))
	b.WriteString("\n\n")
	s.vp.Width = width
	s.vp.Height = max(height-2, 1)
	s.vp.SetContent(wrap(s.text(), width))
	b.WriteString(s.vp.View())
	return b.String()
}

func (s *runScreen) keys() string {
	keys := "↑/↓ scroll · pgup/pgdn page"
	switch {
	case !s.parts.HasDetail():
	case s.showDetail:
		keys += " · d hide the tool's detail"
	default:
		keys += " · d show the tool's detail"
	}
	return keys + " · esc back · q quit"
}
