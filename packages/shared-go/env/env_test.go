package env

import (
	"testing"
	"time"
)

func TestString(t *testing.T) {
	t.Setenv("FOO", "bar")
	if got := String("FOO", "default"); got != "bar" {
		t.Errorf("set: got %q want bar", got)
	}
	t.Setenv("EMPTY", "")
	if got := String("EMPTY", "default"); got != "default" {
		t.Errorf("empty falls back: got %q want default", got)
	}
	if got := String("MISSING_KEY_NOPE", "default"); got != "default" {
		t.Errorf("missing falls back: got %q", got)
	}
}

func TestInt(t *testing.T) {
	t.Setenv("N", "42")
	if got := Int("N", 7); got != 42 {
		t.Errorf("parse: got %d want 42", got)
	}
	t.Setenv("BAD", "not-a-number")
	if got := Int("BAD", 7); got != 7 {
		t.Errorf("unparseable falls back: got %d want 7", got)
	}
	if got := Int("MISSING_KEY_NOPE", 7); got != 7 {
		t.Errorf("missing falls back: got %d want 7", got)
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("D", "1m30s")
	want := 90 * time.Second
	if got := Duration("D", time.Second); got != want {
		t.Errorf("parse: got %v want %v", got, want)
	}
	t.Setenv("BAD", "huh")
	if got := Duration("BAD", time.Second); got != time.Second {
		t.Errorf("unparseable falls back: got %v", got)
	}
}

func TestBool(t *testing.T) {
	for _, truthy := range []string{"1", "true", "TRUE", "yes", "On"} {
		t.Setenv("B", truthy)
		if !Bool("B", false) {
			t.Errorf("%q should be true", truthy)
		}
	}
	for _, falsy := range []string{"0", "false", "FALSE", "no", "Off"} {
		t.Setenv("B", falsy)
		if Bool("B", true) {
			t.Errorf("%q should be false", falsy)
		}
	}
	t.Setenv("WAT", "maybe")
	if !Bool("WAT", true) {
		t.Errorf("unparseable falls back to default true")
	}
	if Bool("WAT", false) {
		t.Errorf("unparseable falls back to default false")
	}
}
