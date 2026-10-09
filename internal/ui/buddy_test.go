package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"lilbuddy/internal/github"
)

func TestBuddyVisibility(t *testing.T) {
	for _, tc := range []struct {
		name          string
		count         int
		width, height int
		loading       bool
		cached        bool
		err           error
		visible       bool
	}{
		{name: "ten remaining", count: 10, width: 80, height: 40, visible: true},
		{name: "above old threshold", count: 11, width: 80, height: 40, visible: true},
		{name: "large inbox with space", count: 30, width: 80, height: 50, visible: true},
		{name: "large inbox without space", count: 30, width: 80, height: 40},
		{name: "empty", width: 80, height: 24, visible: true},
		{name: "short", count: 10, width: 80, height: 24},
		{name: "narrow", count: 1, width: 12, height: 40},
		{name: "initial load", width: 80, height: 40, loading: true},
		{name: "cached empty refresh", width: 80, height: 40, loading: true, cached: true, visible: true},
		{name: "refresh with items", count: 2, width: 80, height: 40, loading: true, visible: true},
		{name: "load error", width: 80, height: 40, err: errors.New("offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(t)
			m.items = make([]github.Item, tc.count)
			m.width, m.height = tc.width, tc.height
			m.loading, m.cached, m.err = tc.loading, tc.cached, tc.err
			padding := m.inboxPadding(m.rows())
			if (strings.TrimSpace(padding) != "") != tc.visible {
				t.Fatal("unexpected companion visibility")
			}
			want := m.rows() - min(m.rows(), max(1, tc.count))
			if strings.Count(padding, "\n") != want {
				t.Fatal("companion changed the reserved row count")
			}
			for _, line := range strings.Split(padding, "\n") {
				if lipgloss.Width(line) > m.width {
					t.Fatal("companion exceeded the available width")
				}
			}
			view := m.View()
			if lipgloss.Height(view) > m.height || (tc.width >= 80 && !strings.Contains(view, "archive")) {
				t.Fatal("companion displaced the inbox footer")
			}
		})
	}
}

func TestBuddyMargins(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3, 11, 30} {
		lines := buddyLines(count, 0)
		width := 0
		for _, line := range lines {
			width = max(width, lipgloss.Width(line))
		}
		m := testModel(t)
		m.items = make([]github.Item, count)
		m.width = width + 4
		rows := max(1, count) + len(lines) + 2
		padding := m.inboxPadding(rows)
		if strings.TrimSpace(padding) == "" {
			t.Fatal("companion hidden despite sufficient margins")
		}
		if !strings.HasPrefix(padding, "\n") || !strings.HasSuffix(padding, "\n\n") {
			t.Fatal("missing vertical breathing room")
		}
		for _, line := range strings.Split(padding, "\n") {
			if strings.TrimSpace(line) != "" && (!strings.HasPrefix(line, "  ") || lipgloss.Width(line) > m.width-2) {
				t.Fatal("missing horizontal breathing room")
			}
		}
		if strings.TrimSpace(m.inboxPadding(rows-1)) != "" {
			t.Fatal("companion shown without enough vertical margin")
		}
		m.width--
		if strings.TrimSpace(m.inboxPadding(rows)) != "" {
			t.Fatal("companion shown without enough horizontal margin")
		}
	}
}

func TestBuddyFramesStayInField(t *testing.T) {
	for frame := 0; frame < buddyPeriod; frame++ {
		for _, remaining := range []int{0, 1} {
			for _, line := range buddyLines(remaining, frame) {
				if lipgloss.Width(line) != buddyFieldWidth {
					t.Fatalf("frame %d escaped its field: %q", frame, line)
				}
			}
		}
	}
	right := strings.Join(buddyLines(1, 0), "\n")
	left := strings.Join(buddyLines(1, buddyTravel), "\n")
	if !strings.Contains(right, "•ᐳ") || !strings.Contains(left, "ᐸ•") {
		t.Fatal("fox did not turn around")
	}
	if !strings.Contains(right, butterfly) {
		t.Fatal("butterfly missing")
	}
	asleep := strings.Join(buddyLines(0, 0), "\n")
	if strings.Contains(asleep, butterfly) || !strings.Contains(asleep, "‿ᐳ") {
		t.Fatal("empty inbox did not put the fox to sleep")
	}
}

func TestBuddyStepsOnlyOnActions(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 80, 40
	m.items = nil
	for i := 0; i < 4; i++ {
		m.items = append(m.items, github.Item{ID: int64(i + 1), Repo: "acme/widgets", Number: i + 1, State: "open", UpdatedAt: time.Now()})
	}
	m.allItems = append([]github.Item(nil), m.items...)
	if cmd := m.Init(); cmd == nil {
		t.Fatal("expected startup commands")
	}
	frame := m.inboxPadding(m.rows())
	for _, key := range []string{"j", "k", "G", "g", "d", "d"} {
		m, _ = press(m, key)
		if m.inboxPadding(m.rows()) != frame {
			t.Fatalf("%q moved the fox", key)
		}
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	if m = next.(Model); m.inboxPadding(m.rows()) != frame {
		t.Fatal("redraw moved the fox")
	}
	for _, key := range []string{"e", "m", "u"} {
		before := m.buddyFrame
		m, _ = press(m, key)
		if m.buddyFrame == before {
			t.Fatalf("%q did not move the fox", key)
		}
	}
}
