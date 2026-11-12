package execute

import "fmt"

// Describe says what running this preview would really do, so the count matches
// what happens: tests that would be skipped are not counted as run. The command
// and the menu both ask their question with it.
func (p Preview) Describe(env string) string {
	if n := len(p.Skipped); n > 0 {
		return fmt.Sprintf("Run %d of %d tests in environment %q (%d skipped)", len(p.Tests), len(p.Tests)+n, env, n)
	}
	return fmt.Sprintf("Run all %d tests in environment %q", len(p.Tests), env)
}
