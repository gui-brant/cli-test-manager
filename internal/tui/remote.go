package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/pull"
)

// defaultCheckEvery is how often an open project asks GitHub whether there are
// runs it has not pulled. One list call, no downloads.
const defaultCheckEvery = 2 * time.Minute

// remote is the state of an open project's link to the service its results are
// pulled from (entry 57). It exists only while a project whose manifest has a
// pull section is open.
type remote struct {
	sel  selection
	busy string // "", "pulling" or "checking"

	connected bool   // the last call to the service worked
	problem   string // why it did not, when it did not

	pulledAt  time.Time
	lastPull  string // what that pull did, in words
	checkedAt time.Time
	newRuns   int // runs the service has that are not pulled yet, as of the last check

	tickPending bool // a timer is already running
}

// Messages the background work sends back. They carry the remote they were for,
// so one that arrives after the project was closed is ignored.
type (
	remotePulled struct {
		r   *remote
		rep pull.Report
		err error
	}
	remoteChecked struct {
		r   *remote
		n   int
		err error
	}
	remoteTick struct{ r *remote }
)

// modalScreen is a screen that takes every key for itself, so the pull key must
// not be taken from it: a question, or a run in progress.
type modalScreen interface{ modal() bool }

// refreshable is a screen that shows runs and can read them again.
type refreshable interface{ refresh() }

func (s selection) pullEnabled() bool {
	d := s.deps
	return s.manifest().Pull != nil && d.NewSource != nil && d.Creds != nil && d.Save != nil
}

func (m *Model) now() time.Time {
	if m.deps.Now != nil {
		return m.deps.Now()
	}
	return time.Now()
}

func (m *Model) context() context.Context {
	if m.deps.Context != nil {
		return m.deps.Context
	}
	return context.Background()
}

// openRemote starts watching a project that was just opened, and pulls from it.
func (m *Model) openRemote(sel selection) tea.Cmd {
	if !sel.pullEnabled() {
		m.remote = nil
		return nil
	}
	m.remote = &remote{sel: sel}
	return m.startPull(m.remote)
}

// startPull pulls in the background. Nothing happens while another call is going.
func (m *Model) startPull(r *remote) tea.Cmd {
	if r.busy != "" {
		return nil
	}
	r.busy = "pulling"
	ctx := m.context()
	return func() tea.Msg {
		rep, err := pullNow(ctx, r.sel)
		return remotePulled{r: r, rep: rep, err: err}
	}
}

// startCheck asks the service, without downloading anything, whether there is
// something to pull.
func (m *Model) startCheck(r *remote) tea.Cmd {
	if r.busy != "" {
		return nil
	}
	r.busy = "checking"
	ctx := m.context()
	return func() tea.Msg {
		n, err := checkNow(ctx, r.sel)
		return remoteChecked{r: r, n: n, err: err}
	}
}

func pullNow(ctx context.Context, sel selection) (pull.Report, error) {
	token, scrub, err := pull.Prepare(ctx, sel.deps.Creds, sel.manifest())
	if err != nil {
		return pull.Report{}, err
	}
	store, err := sel.deps.Save(sel.loaded.HistoryPath)
	if err != nil {
		return pull.Report{}, err
	}
	defer store.Close()
	svc := &pull.Service{Source: sel.deps.NewSource(token), Store: store, Redactor: scrub, Now: sel.deps.Now}
	return svc.Pull(ctx, pull.Request{Manifest: sel.manifest()})
}

func checkNow(ctx context.Context, sel selection) (int, error) {
	token, _, err := pull.Prepare(ctx, sel.deps.Creds, sel.manifest())
	if err != nil {
		return 0, err
	}
	var store core.Store = noHistory{}
	if sel.deps.History != nil {
		opened, err := sel.deps.History(sel.loaded.HistoryPath)
		if err != nil {
			return 0, err
		}
		if opened != nil {
			defer opened.Close()
			store = opened
		}
	}
	svc := &pull.Service{Source: sel.deps.NewSource(token), Store: store, Now: sel.deps.Now}
	return svc.Check(ctx, sel.manifest())
}

// noHistory stands in for a history file that does not exist yet: nothing is
// stored, so everything on the service is new.
type noHistory struct{ core.Store }

func (noHistory) FindByKey(context.Context, string, int) (core.Run, error) {
	return core.Run{}, core.ErrNotFound
}
func (noHistory) ListRuns(context.Context, core.RunFilter) ([]core.RunSummary, error) {
	return nil, nil
}

// tick schedules the next check, unless one is already scheduled.
func (m *Model) tick(r *remote) tea.Cmd {
	if r.tickPending {
		return nil
	}
	r.tickPending = true
	every := m.deps.CheckEvery
	if every <= 0 {
		every = defaultCheckEvery
	}
	return tea.Tick(every, func(time.Time) tea.Msg { return remoteTick{r} })
}

// handleRemote deals with the messages of the link. handled is false for any
// other message.
func (m *Model) handleRemote(msg tea.Msg) (cmd tea.Cmd, handled bool) {
	switch msg := msg.(type) {
	case remotePulled:
		r := msg.r
		if r != m.remote {
			return nil, true
		}
		r.busy = ""
		if errors.Is(msg.err, context.Canceled) {
			return nil, true
		}
		if msg.err != nil {
			r.connected, r.problem = false, problemText(msg.err)
		} else {
			r.connected, r.problem = true, ""
			r.newRuns = 0
		}
		r.pulledAt, r.lastPull = m.now(), pulledWords(msg.rep)
		if msg.rep.New() > 0 || msg.rep.Pending > 0 {
			m.refreshScreens()
		}
		return m.tick(r), true

	case remoteChecked:
		r := msg.r
		if r != m.remote {
			return nil, true
		}
		r.busy = ""
		if errors.Is(msg.err, context.Canceled) {
			return nil, true
		}
		if msg.err != nil {
			r.connected, r.problem = false, problemText(msg.err)
		} else {
			r.connected, r.problem, r.newRuns = true, "", msg.n
		}
		r.checkedAt = m.now()
		return m.tick(r), true

	case remoteTick:
		r := msg.r
		r.tickPending = false
		if r != m.remote {
			return nil, true
		}
		if r.busy != "" {
			return m.tick(r), true
		}
		return m.startCheck(r), true
	}
	return nil, false
}

// refreshScreens makes the screens that list runs read them again.
func (m *Model) refreshScreens() {
	for _, s := range m.stack {
		if rs, ok := s.(refreshable); ok {
			rs.refresh()
		}
	}
}

// problemText is why the link is down, short enough for one line: without a
// prefix that says nothing, and without the advice that follows a semicolon (the
// command prints that in full).
func problemText(err error) string {
	text := strings.TrimPrefix(err.Error(), "pull: ")
	text = strings.TrimPrefix(text, "github: ")
	text = strings.TrimSuffix(text, " (its value is never stored)")
	if before, _, ok := strings.Cut(text, "; "); ok {
		text = before
	}
	return text
}

func pulledWords(rep pull.Report) string {
	switch {
	case rep.New() == 0 && rep.Pending == 0:
		return "nothing new"
	case rep.New() == 0:
		return noun(rep.Pending, "run still going", "runs still going")
	}
	return noun(rep.New(), "new run", "new runs")
}

// line is the footer that says how the link is doing.
func (r *remote) line(zone *time.Location) string {
	at := func(t time.Time) string {
		if zone != nil {
			t = t.In(zone)
		}
		return t.Format("15:04")
	}
	switch {
	case r.busy == "pulling":
		return "GitHub: pulling..."
	case r.busy == "checking":
		return "GitHub: checking for new runs..."
	case !r.connected && r.problem != "":
		return "GitHub: not connected: " + r.problem
	case !r.connected:
		return "GitHub: connecting..."
	}
	parts := []string{"GitHub: connected"}
	if !r.pulledAt.IsZero() {
		parts = append(parts, fmt.Sprintf("pulled %s (%s)", at(r.pulledAt), r.lastPull))
	}
	if r.checkedAt.After(r.pulledAt) {
		parts = append(parts, "checked "+at(r.checkedAt))
	}
	if r.newRuns > 0 {
		parts = append(parts, fmt.Sprintf("%s on GitHub, not pulled yet", noun(r.newRuns, "new run", "new runs")))
	}
	return strings.Join(parts, " · ")
}

// pullKey is how the pull key shows in the key line: with a white dot when the
// service has runs that are not pulled yet.
func (r *remote) pullKey() string {
	if r.connected && r.newRuns > 0 {
		return "p pull ●"
	}
	return "p pull"
}
