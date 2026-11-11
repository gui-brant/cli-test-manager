# Bubble Tea, patched copy

This folder is a copy of `github.com/charmbracelet/bubbletea` **v1.3.10**
(MIT licence, see `LICENSE`), used through a `replace` line in the module's
`go.mod`. Only the library's non-test source files are copied.

## The one change
`tea_init.go` is **removed**. That file held one `init()` function that asked
the terminal for its background colour as soon as any program that includes
Bubble Tea started. On a terminal that never answers (the Linux console, some
embedded terminals) every command of this program would then wait about five
seconds before doing anything, push hooks included.

The function exists to stop a different hang: Lip Gloss asks for the
background colour lazily, and if that happens while Bubble Tea owns the
terminal, Bubble Tea swallows the answer and Lip Gloss waits for it in vain. This
program uses no colour that depends on the background, and the menu sets the
background explicitly (`lipgloss.SetHasDarkBackground`) before it draws, so
nothing ever asks later and the early question has no purpose here.

A test (`internal/tui/patch_test.go`) fails if `tea_init.go` comes back or the
`replace` line disappears.

## Upgrading
1. Copy the new version's non-test `.go` files, `go.mod` and `LICENSE` over this
   folder.
2. Delete `tea_init.go` again (and check the new version for any other
   start-up query: search it for `HasDarkBackground`).
3. Update the version in this note, run `go mod tidy` and `go test ./...`.

Bubble Tea v2 removes the workaround, and with it the need for this copy: when
the program moves to v2, delete this folder and the `replace` line.
