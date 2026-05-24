// Package env reads typed values out of process environment variables
// with caller-supplied defaults. Zero dependencies so it stays cheap to
// import from any animal package.
package env

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// String returns the value of $k, or d if unset / empty.
func String(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// Int returns $k parsed as an int, or d if unset / unparseable.
func Int(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

// Duration returns $k parsed via time.ParseDuration, or d if unset /
// unparseable.
func Duration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if dd, err := time.ParseDuration(v); err == nil {
			return dd
		}
	}
	return d
}

// Bool returns $k parsed as a boolean. Accepts case-insensitive
// 1/true/yes/on for true and 0/false/no/off for false. Anything else
// (including unset) returns d.
func Bool(k string, d bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return d
}
