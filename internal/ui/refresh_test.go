package ui

import (
	"errors"
	"testing"
	"time"
)

func TestRefreshDue(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name         string
		interval     time.Duration
		idle         time.Duration
		sinceRefresh time.Duration
		want         bool
	}{
		{"active before deadline", 5 * time.Minute, time.Minute, 4 * time.Minute, false},
		{"active at deadline", 5 * time.Minute, time.Minute, 5 * time.Minute, true},
		{"just active", 5 * time.Minute, activityWindow - time.Nanosecond, 5 * time.Minute, true},
		{"idle boundary", 5 * time.Minute, activityWindow, 5 * time.Minute, false},
		{"idle before deadline", 5 * time.Minute, 2 * time.Hour, time.Hour - time.Nanosecond, false},
		{"idle at deadline", 5 * time.Minute, 2 * time.Hour, time.Hour, true},
		{"custom active interval", 2 * time.Minute, time.Minute, 2 * time.Minute, true},
		{"custom interval still backs off", 2 * time.Minute, 10 * time.Minute, 2 * time.Minute, false},
		{"long interval is preserved", 2 * time.Hour, 3 * time.Hour, time.Hour, false},
		{"disabled while active", 0, time.Minute, 2 * time.Hour, false},
		{"disabled while idle", 0, 2 * time.Hour, 2 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				interval:           tc.interval,
				lastInteraction:    now.Add(-tc.idle),
				lastRefreshAttempt: now.Add(-tc.sinceRefresh),
			}
			if got := m.refreshDue(now); got != tc.want {
				t.Fatalf("refreshDue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIdleRefreshAndResume(t *testing.T) {
	m := testModel(t)
	m.interval = 5 * time.Minute
	m.lastInteraction = time.Now().Add(-10 * time.Minute)
	m.lastRefreshAttempt = m.lastInteraction
	lastAttempt := m.lastRefreshAttempt
	next, cmd := m.Update(tick{})
	m = next.(Model)
	if m.loading || cmd == nil || !m.lastRefreshAttempt.Equal(lastAttempt) {
		t.Fatal("idle tick refreshed early or stopped scheduling checks")
	}
	if !m.lastInteraction.Equal(lastAttempt) {
		t.Fatal("background tick counted as interaction")
	}

	before := time.Now()
	m, _ = press(m, "j")
	if m.lastInteraction.Before(before) {
		t.Fatal("navigation did not record interaction")
	}
	next, cmd = m.Update(tick{})
	m = next.(Model)
	if !m.loading || cmd == nil || !m.lastRefreshAttempt.After(lastAttempt) {
		t.Fatal("returning to the app did not restore the active cadence")
	}

	lastAttempt = m.lastRefreshAttempt
	next, _ = m.Update(tick{})
	m = next.(Model)
	if !m.lastRefreshAttempt.Equal(lastAttempt) {
		t.Fatal("tick started an overlapping refresh")
	}
}

func TestHourlyRefreshDoesNotCountAsInteraction(t *testing.T) {
	m := testModel(t)
	m.interval = 5 * time.Minute
	m.lastInteraction = time.Now().Add(-2 * time.Hour)
	m.lastRefreshAttempt = m.lastInteraction
	lastInteraction := m.lastInteraction
	next, cmd := m.Update(tick{})
	m = next.(Model)
	if !m.loading || cmd == nil || !m.lastInteraction.Equal(lastInteraction) {
		t.Fatal("hourly refresh did not start or counted as interaction")
	}
	next, _ = m.Update(loaded{err: errors.New("offline")})
	m = next.(Model)
	if m.refreshDue(time.Now().Add(5 * time.Minute)) {
		t.Fatal("failed idle refresh retried at the active cadence")
	}
	if !m.lastInteraction.Equal(lastInteraction) {
		t.Fatal("refresh completion counted as interaction")
	}
}

func TestManualRefreshWhileIdleOrDisabled(t *testing.T) {
	for _, interval := range []time.Duration{0, 5 * time.Minute} {
		t.Run(interval.String(), func(t *testing.T) {
			m := testModel(t)
			m.interval = interval
			m.lastInteraction = time.Now().Add(-10 * time.Minute)
			before := time.Now()
			m, cmd := press(m, "r")
			if !m.loading || cmd == nil || m.lastRefreshAttempt.Before(before) || m.lastInteraction.Before(before) {
				t.Fatal("manual refresh did not immediately start and record activity")
			}
			if interval == 0 && m.nextTick() != nil {
				t.Fatal("manual refresh re-enabled automatic refresh")
			}
		})
	}
}
