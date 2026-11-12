package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/execute"
	"github.com/gui-brant/cli-test-manager/internal/query"
)

// canRun says whether the menu was given what running tests needs.
func (s selection) canRun() error {
	if s.deps.Runner == nil || s.deps.Creds == nil || s.deps.Save == nil {
		return errors.New("this menu was started without a way to run tests")
	}
	return nil
}

func (s selection) service(store core.Store) *execute.Service {
	return &execute.Service{Runner: s.deps.Runner, Creds: s.deps.Creds, Environ: s.deps.Environ, Now: s.deps.Now, Store: store}
}

func (s selection) request(pick execute.Selection) execute.Request {
	return execute.Request{Manifest: s.manifest(), Root: s.loaded.Root, Environment: s.env, Select: pick}
}

// preview checks a request without running anything, as the run command does
// before it asks anyone to confirm.
func (s selection) preview(req execute.Request) (execute.Preview, error) {
	if err := s.canRun(); err != nil {
		return execute.Preview{}, err
	}
	p, err := s.service(nil).Preview(context.Background(), req)
	if err != nil {
		// Drop the package's prefix; the screen says whose problem it is.
		return p, errors.New(strings.TrimPrefix(err.Error(), "execute: "))
	}
	return p, nil
}

// ---- run all: ask first ----

// confirmScreen asks "are you sure" about running every test of the feature, with
// the count the run would really have (entry 55).
type confirmScreen struct {
	sel     selection
	req     execute.Request
	preview execute.Preview
}

func newRunAllScreen(sel selection) (*confirmScreen, error) {
	req := sel.request(execute.Selection{Feature: sel.feature})
	p, err := sel.preview(req)
	if err != nil {
		return nil, err
	}
	return &confirmScreen{sel: sel, req: req, preview: p}, nil
}

func (s *confirmScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nav{}, nil
	}
	if k.String() == "y" {
		p := newProgressScreen(s.sel, s.req, s.preview)
		return s, nav{replace: p}, p.start()
	}
	return s, nav{pop: true}, nil // anything but yes is no
}

func (s *confirmScreen) view(width, height int) string {
	var b strings.Builder
	b.WriteString(clip(s.sel.breadcrumb(), width))
	b.WriteString("\n")
	b.WriteString(clip("Run all", width))
	b.WriteString("\n\n")
	b.WriteString(wrap(s.preview.Describe(s.sel.env)+"? (y/N)", width))
	for _, sk := range s.preview.Skipped {
		b.WriteString("\n")
		b.WriteString(wrap(fmt.Sprintf("  skipped  %s  (%s)", sk.Test.ID, sk.Reason), width))
	}
	return b.String()
}

func (s *confirmScreen) keys() string { return "y run · any other key goes back" }

// ---- run several: choose the tests ----

// severalScreen lists the tests of the feature that can run in the environment,
// each with a box, and runs the ones that are ticked (entry 55).
type severalScreen struct {
	sel     selection
	ids     []string
	names   []string
	checked []bool
	pick    *picker
	status  string
}

func newSeveralScreen(sel selection) (*severalScreen, error) {
	if err := sel.canRun(); err != nil {
		return nil, err
	}
	rows, err := query.Tests(sel.manifest(), sel.feature)
	if err != nil {
		return nil, err
	}
	s := &severalScreen{sel: sel}
	var items []pickItem
	for _, r := range rows {
		if !slices.Contains(sel.manifest().Runners[r.Runner].Environments, sel.env) {
			continue // it cannot run here, so it cannot be chosen
		}
		s.ids = append(s.ids, r.ID)
		s.names = append(s.names, fmt.Sprintf("#%d %s", r.Number, r.Name))
		s.checked = append(s.checked, false)
		items = append(items, pickItem{label: "[ ] " + s.names[len(s.names)-1], hint: r.ID})
	}
	s.pick = newPicker(items)
	return s, nil
}

func (s *severalScreen) mark(i int) {
	box := "[ ] "
	if s.checked[i] {
		box = "[x] "
	}
	s.pick.setLabel(i, box+s.names[i])
}

func (s *severalScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	if n, ok := commonKeys(msg); ok {
		return s, n, nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nav{}, s.pick.update(msg)
	}
	s.status = ""
	switch k.String() {
	case " ":
		if i := s.pick.at(); i >= 0 {
			s.checked[i] = !s.checked[i]
			s.mark(i)
		}
		return s, nav{}, nil
	case "a": // all, or none when all are ticked already
		all := slices.Contains(s.checked, false)
		for i := range s.checked {
			s.checked[i] = all
			s.mark(i)
		}
		return s, nav{}, nil
	case "enter":
		var ids []string
		for i, on := range s.checked {
			if on {
				ids = append(ids, s.ids[i])
			}
		}
		if len(ids) == 0 {
			s.status = "Choose at least one test with the space key (a chooses all of them)."
			return s, nav{}, nil
		}
		req := s.sel.request(execute.Selection{TestIDs: ids})
		preview, err := s.sel.preview(req)
		if err != nil {
			s.status = err.Error()
			return s, nav{}, nil
		}
		p := newProgressScreen(s.sel, req, preview)
		return s, nav{push: p}, p.start()
	}
	return s, nav{}, s.pick.update(msg)
}

func (s *severalScreen) view(width, height int) string {
	return layoutList(width, height, s.sel.breadcrumb(), fmt.Sprintf("Choose the tests of %s to run in %s", s.sel.feature, s.sel.env),
		s.pick, s.status, fmt.Sprintf("No test of %s can run in %s.", s.sel.feature, s.sel.env))
}

func (s *severalScreen) keys() string {
	if len(s.pick.items) == 0 {
		return "esc back · q quit"
	}
	return "↑/↓ move · space tick · a all · enter run the ticked · esc back · q quit"
}

// ---- a run in progress ----

type progressState int

const (
	running progressState = iota
	cancelling
	stopped // finished without a saved run: cancelled, or it could not start or save
)

// runEvent and runDone carry what the run reports back to the screen.
type runEvent struct{ ev execute.Event }
type runDone struct {
	rep execute.Report
	err error
}

// progressScreen runs the tests and lists them as they go. When the run is saved
// it is replaced by the run's own screen. Esc or Ctrl-C stops the run and saves
// nothing (entry 55).
type progressScreen struct {
	sel     selection
	req     execute.Request
	preview execute.Preview
	state   progressState
	lines   []string // what is shown, one line each; the last started test is updated in place
	current int      // index of the line of the test running, or -1
	status  string
	cancel  context.CancelFunc
	events  chan tea.Msg
}

func newProgressScreen(sel selection, req execute.Request, p execute.Preview) *progressScreen {
	return &progressScreen{sel: sel, req: req, preview: p, current: -1}
}

func (p *progressScreen) busy() bool { return p.state != stopped }

// start begins the run in the background and returns the command that waits for
// its first report.
func (p *progressScreen) start() tea.Cmd {
	ctx := p.sel.deps.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, p.cancel = context.WithCancel(ctx)
	p.events = make(chan tea.Msg, 64)
	events, req, sel := p.events, p.req, p.sel
	go func() {
		store, err := sel.deps.Save(sel.loaded.HistoryPath)
		if err != nil {
			events <- runDone{err: err}
			return
		}
		defer store.Close()
		req.Progress = func(ev execute.Event) { events <- runEvent{ev} }
		rep, err := sel.service(store).Run(ctx, req)
		events <- runDone{rep: rep, err: err}
	}()
	return p.wait()
}

func (p *progressScreen) wait() tea.Cmd {
	ch := p.events
	return func() tea.Msg { return <-ch }
}

func (p *progressScreen) update(msg tea.Msg) (screen, nav, tea.Cmd) {
	switch msg := msg.(type) {
	case runEvent:
		p.apply(msg.ev)
		return p, nav{}, p.wait()
	case runDone:
		return p.finish(msg)
	case tea.KeyMsg:
		if p.state != stopped {
			if k := msg.String(); k == "esc" || k == "ctrl+c" {
				p.state = cancelling
				p.cancel()
			}
			return p, nav{}, nil // q and the rest do nothing until the run has stopped
		}
		if n, ok := commonKeys(msg); ok {
			return p, n, nil
		}
	}
	return p, nav{}, nil
}

func (p *progressScreen) apply(ev execute.Event) {
	switch ev.Kind {
	case execute.EventSkipped:
		p.lines = append(p.lines, fmt.Sprintf("skipped  %s  (%s)", ev.Test.ID, ev.Reason))
	case execute.EventStarted:
		p.lines = append(p.lines, fmt.Sprintf("[%d/%d] %s ... running", ev.Index, ev.Total, ev.Test.ID))
		p.current = len(p.lines) - 1
	case execute.EventFinished:
		result := "ok"
		if ev.Failed {
			result = "FAILED: " + ev.Reason
		}
		if p.current >= 0 {
			p.lines[p.current] = fmt.Sprintf("[%d/%d] %s ... %s", ev.Index, ev.Total, ev.Test.ID, result)
		}
	}
}

func (p *progressScreen) finish(d runDone) (screen, nav, tea.Cmd) {
	p.state = stopped
	switch {
	case d.err == nil && d.rep.Saved:
		next, err := newRunScreen(p.sel, d.rep.RunID)
		if err == nil {
			return p, nav{replace: next}, nil
		}
		p.status = fmt.Sprintf("Run %d was saved, but it cannot be shown: %v", d.rep.RunID, err)
	case errors.Is(d.err, context.Canceled):
		p.status = "The run was cancelled; nothing was saved."
	case len(d.rep.Run.Results) > 0:
		p.status = fmt.Sprintf("The tests ran but the run could not be saved: %v", strings.TrimPrefix(fmt.Sprint(d.err), "execute: "))
	default:
		p.status = strings.TrimPrefix(fmt.Sprint(d.err), "execute: ")
	}
	return p, nav{}, nil
}

func (p *progressScreen) view(width, height int) string {
	title := fmt.Sprintf("Running %s in %s", noun(len(p.preview.Tests), "test", "tests"), p.sel.env)
	switch p.state {
	case cancelling:
		title = "Stopping the run..."
	case stopped:
		title = "The run stopped"
	}
	var b strings.Builder
	b.WriteString(clip(p.sel.breadcrumb(), width))
	b.WriteString("\n")
	b.WriteString(clip(title, width))
	b.WriteString("\n\n")
	// Keep the newest lines when there are more than fit.
	room := max(height-5, 1)
	lines := p.lines
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	for _, l := range lines {
		b.WriteString(clip(l, width))
		b.WriteString("\n")
	}
	if p.status != "" {
		b.WriteString("\n")
		b.WriteString(wrap(p.status, width))
	}
	return b.String()
}

func (p *progressScreen) keys() string {
	switch p.state {
	case running:
		return "esc or ctrl+c cancels the run; nothing is saved"
	case cancelling:
		return "waiting for the test command to stop"
	}
	return "esc back · q quit"
}
