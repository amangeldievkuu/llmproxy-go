package main

import (
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	const day = 24 * time.Hour
	for s, want := range map[string]time.Duration{
		"90d":   90 * day,
		"7d12h": 7*day + 12*time.Hour,
		"1.5h":  90 * time.Minute,
		"0d":    0,
	} {
		if got, err := parseAge(s); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "d", "1.5d", "-3d", "abc", "7d-1h", "999999999d"} {
		if _, err := parseAge(s); err == nil {
			t.Errorf("parseAge(%q): expected an error", s)
		}
	}
}

func TestAgeString(t *testing.T) {
	if got := age(90 * 24 * time.Hour).String(); got != "90d" {
		t.Errorf("String() = %q, want 90d", got)
	}
	if got := age(36 * time.Hour).String(); got != "36h0m0s" {
		t.Errorf("String() = %q, want 36h0m0s", got)
	}
}

func TestMoment(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.Local)
	for s, want := range map[string]time.Time{
		"2026-09-01": time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local),
		"7d":         now.Add(-7 * 24 * time.Hour),
		"90m":        now.Add(-90 * time.Minute),
	} {
		var m moment
		if err := m.Set(s); err != nil || !m.at(now).Equal(want) {
			t.Errorf("moment %q = %v, %v; want %v", s, m.at(now), err, want)
		}
	}
	for _, s := range []string{"2026-13-01", "2026/09/01", "soon"} {
		var m moment
		if err := m.Set(s); err == nil {
			t.Errorf("moment %q: expected an error", s)
		}
	}
	if got := (moment{}).at(now); !got.Equal(now) {
		t.Errorf("unset moment = %v, want now", got)
	}
}
