package match

import "testing"

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		// A plain path, and ordinary segment wildcards.
		{"tests/a.sql", "tests/a.sql", true},
		{"tests/a.sql", "tests/b.sql", false},
		{"tests/*.sql", "tests/a.sql", true},
		{"tests/*.sql", "tests/sub/a.sql", false}, // * stays inside one folder
		{"tests/?.sql", "tests/a.sql", true},
		{"tests/?.sql", "tests/ab.sql", false},
		{"tests/[ab].sql", "tests/b.sql", true},
		{"tests/[ab].sql", "tests/c.sql", false},

		// ** is any number of whole folders, none included.
		{"tests/**", "tests/a.sql", true},
		{"tests/**", "tests/x/y/z.sql", true},
		{"tests/**", "other/a.sql", false},
		{"supabase/**/*.sql", "supabase/a.sql", true},
		{"supabase/**/*.sql", "supabase/migrations/a.sql", true},
		{"supabase/**/*.sql", "supabase/a/b/c/a.sql", true},
		{"supabase/**/*.sql", "supabase/a/b/c/a.txt", false},
		{"supabase/**/*.sql", "other/a.sql", false},
		{"**/login*", "login.ts", true},
		{"**/login*", "src/auth/login_form.ts", true},
		{"**/login*", "src/auth/signin.ts", false},
		{"**", "anything/at/all", true},
		{"a/**/b/**/c", "a/b/c", true},
		{"a/**/b/**/c", "a/x/b/y/z/c", true},
		{"a/**/b/**/c", "a/x/c", false},
		{"a/**/**/b", "a/b", true}, // consecutive ** collapse

		// A segment that merely contains ** is an ordinary segment.
		{"a/**b/c", "a/xb/c", true},
		{"a/**b/c", "a/x/y/b/c", false},

		// Whole segments only: a pattern is never a prefix match.
		{"tests", "tests/a.sql", false},
		{"tests/a", "tests", false},
		{"tests/*", "tests", false},

		// A malformed pattern matches nothing.
		{"tests/[a", "tests/a", false},
	}
	for _, c := range cases {
		if got := Glob(c.pattern, c.name); got != c.want {
			t.Errorf("Glob(%q, %q) = %v; want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestGlobAny(t *testing.T) {
	patterns := []string{"supabase/**/*login*.sql", "tests/**/login*"}
	for name, want := range map[string]bool{
		"supabase/migrations/20261116_login_a.sql": true,
		"tests/api/login_test.sh":                  true,
		"supabase/migrations/20261116_other.sql":   false,
		"docs/login.md":                            false,
	} {
		if got := GlobAny(patterns, name); got != want {
			t.Errorf("GlobAny(%q) = %v; want %v", name, got, want)
		}
	}
	if GlobAny(nil, "a") {
		t.Error("no patterns must match nothing")
	}
}
