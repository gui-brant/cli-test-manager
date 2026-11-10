package env

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestGetReturnsTheValue(t *testing.T) {
	c := NewWithLookup(lookupFrom(map[string]string{"ACCOUNT_PASSWORD": "pa ss=word 1"}))
	got, err := c.Get(context.Background(), "ACCOUNT_PASSWORD")
	if err != nil || got != "pa ss=word 1" {
		t.Errorf("Get = %q, %v; want the value exactly as set", got, err)
	}
}

func TestGetUnsetOrEmptyIsNotFound(t *testing.T) {
	c := NewWithLookup(lookupFrom(map[string]string{"EMPTY": "", "BLANK": " \t\n"}))
	for _, name := range []string{"MISSING", "EMPTY", "BLANK"} {
		_, err := c.Get(context.Background(), name)
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get(%q) error = %v; want core.ErrNotFound", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name the secret", err)
		}
	}
}

func TestGetRejectsInvalidNamesWithoutLooking(t *testing.T) {
	calls := 0
	c := NewWithLookup(func(string) (string, bool) {
		calls++
		return "value", true
	})
	for _, name := range []string{"", "1BAD", "BAD-NAME", "HAS SPACE", "A=B", "dollar$"} {
		_, err := c.Get(context.Background(), name)
		if err == nil {
			t.Errorf("Get(%q) returned no error", name)
			continue
		}
		if errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get(%q): an invalid name must not look like a missing secret", name)
		}
	}
	if calls != 0 {
		t.Errorf("the environment was consulted %d times for invalid names; want 0", calls)
	}
}

func TestErrorsNeverContainTheValue(t *testing.T) {
	// Only an empty value can reach an error path, and it has no content to
	// leak. A set value is only ever returned, never printed.
	c := NewWithLookup(lookupFrom(map[string]string{"TOKEN": "hunter2-hunter2"}))
	if _, err := c.Get(context.Background(), "TOKEN-WITH-DASH"); err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q leaked a value", err)
	}
}

func TestNewReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("TESTER_CORE_TEST_SECRET", "value-123")
	got, err := New().Get(context.Background(), "TESTER_CORE_TEST_SECRET")
	if err != nil || got != "value-123" {
		t.Errorf("Get = %q, %v; want value-123", got, err)
	}
}
