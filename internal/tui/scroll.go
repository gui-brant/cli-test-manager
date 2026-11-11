package tui

import (
	bkey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// scroller is a block of text that scrolls with the arrows and the page keys. The
// run screen and the code view both use it.
type scroller struct{ vp viewport.Model }

func newScroller() scroller {
	vp := viewport.New(80, 20)
	// The viewport pages with d and u; on the run screen d is the detail key, so
	// no screen leaves them to it.
	vp.KeyMap.HalfPageDown = bkey.NewBinding(bkey.WithKeys("ctrl+d"))
	vp.KeyMap.HalfPageUp = bkey.NewBinding(bkey.WithKeys("ctrl+u"))
	vp.KeyMap.Left = bkey.NewBinding()
	vp.KeyMap.Right = bkey.NewBinding()
	return scroller{vp: vp}
}

func (s *scroller) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return cmd
}

// render shows text, wrapped to the width, in a space this wide and this high,
// keeping the scroll position.
func (s *scroller) render(text string, width, height int) string {
	s.vp.Width = width
	s.vp.Height = max(height, 1)
	s.vp.SetContent(wrap(text, width))
	return s.vp.View()
}
