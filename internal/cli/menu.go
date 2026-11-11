package cli

import (
	"context"
	"errors"
)

// cmdMenu opens the interactive menu. It needs a person at a terminal: a script
// or a push hook cannot use it, so there it refuses at once instead of hanging.
func (a *app) cmdMenu(ctx context.Context, args []string) int {
	if len(args) > 0 {
		return a.errorf("menu takes no arguments; %q is unexpected", args[0])
	}
	if !a.IsTerminal {
		return a.errorf("the menu needs a terminal: run it yourself, not from a script or a hook")
	}
	if a.ProjectsFile == "" {
		return a.errorf("the list of projects is not available: this user has no config folder")
	}
	if a.Menu == nil {
		return a.errorf("this build of the program has no menu")
	}
	if err := a.Menu(ctx, a.Stdin, a.Stdout); err != nil {
		if errors.Is(err, context.Canceled) {
			return ExitCancelled
		}
		return a.errorf("%v", err)
	}
	return ExitOK
}
