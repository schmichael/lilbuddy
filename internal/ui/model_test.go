package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"lilbuddy/internal/github"
	"lilbuddy/internal/state"
)

func testModel(t *testing.T) Model {
	t.Helper()
	store, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := github.New("test", 100)
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), client, store, "me", 0)
	m.loading = false
	m.items = []github.Item{
		{ID: 101, Repo: "acme/widgets", Number: 1, State: "open", Title: "First", URL: "https://github.com/acme/widgets/issues/1", UpdatedAt: time.Now()},
		{ID: 102, Repo: "acme/widgets", Number: 2, State: "open", Title: "Second", URL: "https://github.com/acme/widgets/issues/2", UpdatedAt: time.Now()},
	}
	m.allItems = append([]github.Item(nil), m.items...)
	return m
}

func press(m Model, key string) (Model, tea.Cmd) {
	var msg tea.KeyMsg
	switch key {
	case "up":
		msg.Type = tea.KeyUp
	case "down":
		msg.Type = tea.KeyDown
	default:
		msg.Type, msg.Runes = tea.KeyRunes, []rune(key)
	}
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func TestNavigation(t *testing.T) {
	m := testModel(t)
	for _, key := range []string{"j", "j", "down"} {
		m, _ = press(m, key)
		if m.cursor != 1 {
			t.Fatalf("%s moved cursor out of bounds: %d", key, m.cursor)
		}
	}
	for _, key := range []string{"k", "k", "up"} {
		m, _ = press(m, key)
		if m.cursor != 0 {
			t.Fatalf("%s moved cursor out of bounds: %d", key, m.cursor)
		}
	}
	m, _ = press(m, "G")
	if m.cursor != 1 {
		t.Fatal("G didn't select last item")
	}
	m, _ = press(m, "g")
	if m.cursor != 0 {
		t.Fatal("g didn't select first item")
	}
}

func TestArchiveAndMutePersist(t *testing.T) {
	for _, tc := range []struct {
		key    string
		action state.Action
	}{{"e", state.Archived}, {"m", state.Muted}} {
		t.Run(tc.key, func(t *testing.T) {
			m := testModel(t)
			item := m.items[0]
			m, _ = press(m, tc.key)
			if len(m.items) != 1 || m.items[0].Number != 2 || m.cursor != 0 {
				t.Fatalf("item not removed: %+v", m.items)
			}
			entry := m.store.Items[item.StateKey("me")]
			if entry.Action != tc.action || !entry.UpdatedAt.Equal(item.UpdatedAt) {
				t.Fatalf("bad saved action: %+v", entry)
			}
			m, _ = press(m, tc.key)
			m, _ = press(m, tc.key) // Empty inbox is harmless.
			if len(m.items) != 0 || m.cursor != 0 {
				t.Fatal("bad empty inbox")
			}
		})
	}
}

func TestOpenSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		m := testModel(t)
		item := m.items[0]
		m.open = func(_ context.Context, target string) error {
			if target != item.URL {
				t.Errorf("wrong browser URL %q", target)
			}
			if fail {
				return errors.New("no browser")
			}
			return nil
		}
		m, cmd := press(m, "o")
		if cmd == nil || !m.opening || len(m.items) != 2 {
			t.Fatal("did not launch async browser command")
		}
		next, _ := m.Update(cmd())
		m = next.(Model)
		if fail {
			if len(m.items) != 2 || len(m.store.Items) != 0 || m.err == nil {
				t.Fatal("failed browser launch hid item")
			}
		} else if len(m.items) != 1 || m.store.Items[item.StateKey("me")].Action != state.Opened {
			t.Fatal("successful browser launch didn't persist open action")
		}
	}
}

func TestRefreshDoesNotUndoTriage(t *testing.T) {
	m := testModel(t)
	old := append([]github.Item(nil), m.items...)
	m.loading = true
	m, _ = press(m, "m")
	next, cmd := m.Update(loaded{result: github.Result{Items: old}, revision: 0})
	m = next.(Model)
	if len(m.items) != 1 || cmd == nil || !m.loading {
		t.Fatal("stale refresh restored a muted item")
	}
}

func TestRefreshFailureKeepsInbox(t *testing.T) {
	m := testModel(t)
	next, _ := m.Update(loaded{err: errors.New("offline"), revision: m.revision})
	m = next.(Model)
	if len(m.items) != 2 || m.err == nil {
		t.Fatal("failed refresh discarded inbox")
	}
}

func TestRendering(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 60, 12
	m.items[0].Title = "malicious\x1b[31m\n" + strings.Repeat("long title ", 100)
	view := m.View()
	if strings.Contains(view, "\x1b[31m") || strings.Count(view, "\n") > m.height {
		t.Fatal("unsafe or overflowing output")
	}
	if !strings.Contains(view, "lilbuddy") || !strings.Contains(view, "archive") {
		t.Fatal("missing title or key hints")
	}
}

func TestFormatAge(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{-time.Minute, "now"},
		{0, "now"},
		{time.Minute - time.Nanosecond, "now"},
		{time.Minute, "1m"},
		{5*time.Minute + 59*time.Second, "5m"},
		{time.Hour, "1h"},
		{90*time.Minute + 30*time.Second, "1h30m"},
		{48*time.Hour - time.Nanosecond, "47h59m"},
		{48 * time.Hour, "48h"},
		{48*time.Hour + time.Nanosecond, "2d"},
		{71 * time.Hour, "2d"},
		{72 * time.Hour, "3d"},
		{365 * 24 * time.Hour, "365d"},
	} {
		t.Run(tc.age.String(), func(t *testing.T) {
			if got := formatAge(tc.age); got != tc.want {
				t.Fatalf("formatAge(%s) = %q, want %q", tc.age, got, tc.want)
			}
		})
	}
}

func TestRenderingAgeAfterRefresh(t *testing.T) {
	m := testModel(t)
	m.items[0].Kind = "Issue"
	m.items[0].UpdatedAt = time.Now().Add(-73 * time.Hour)
	if view := m.View(); !strings.Contains(view, "> 3d Issue acme/widgets#1") || !strings.Contains(view, "  now ") {
		t.Fatalf("missing age prefixes: %s", view)
	}
	items := append([]github.Item(nil), m.items...)
	items[0].UpdatedAt = time.Now().Add(-5*time.Minute - 10*time.Second)
	next, _ := m.Update(loaded{result: github.Result{Items: items}, revision: m.revision})
	m = next.(Model)
	if view := m.View(); !strings.Contains(view, "> 5m Issue acme/widgets#1") {
		t.Fatalf("age did not reflect refreshed update time: %s", view)
	}
}

func TestOpenRejectsUnsafeURLs(t *testing.T) {
	for _, target := range []string{"file:///etc/passwd", "https://github.com.evil.example/x", "javascript:alert(1)"} {
		if err := OpenBrowser(context.Background(), target); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
}

func TestDraftToggleIsLocalAndDefaultsToHidden(t *testing.T) {
	m := testModel(t)
	data := append([]github.Item(nil), m.allItems...)
	data[0].Kind, data[0].Draft = "PR", true
	next, _ := m.Update(loaded{result: github.Result{Items: data}})
	m = next.(Model)
	if m.showDrafts || len(m.items) != 1 || m.items[0].Number != 2 {
		t.Fatal("drafts were not hidden by default")
	}
	m, cmd := press(m, "d")
	if cmd != nil || !m.showDrafts || len(m.items) != 2 || len(m.store.Items) != 0 {
		t.Fatal("draft toggle fetched or changed triage state")
	}
	if m.items[m.cursor].Number != 2 {
		t.Fatal("toggle didn't preserve selection")
	}
	if !strings.Contains(m.View(), "[draft]") {
		t.Fatal("draft row was not labeled")
	}
	m, cmd = press(m, "d")
	if cmd != nil || m.showDrafts || len(m.items) != 1 || len(m.store.Items) != 0 {
		t.Fatal("second toggle didn't hide drafts locally")
	}
}

func TestDraftToggleSurvivesRefreshAndMute(t *testing.T) {
	m := testModel(t)
	data := append([]github.Item(nil), m.allItems...)
	data[0].Kind, data[0].Draft = "PR", true
	m, _ = press(m, "d")
	next, _ := m.Update(loaded{result: github.Result{Items: data}})
	m = next.(Model)
	if !m.showDrafts || len(m.items) != 2 {
		t.Fatal("refresh reset draft visibility")
	}
	m, _ = press(m, "g")
	m, _ = press(m, "m")
	m, _ = press(m, "d")
	m, _ = press(m, "d")
	if len(m.items) != 1 || m.items[0].Draft {
		t.Fatal("draft toggle restored a muted draft")
	}
}

func TestClosedItemsStayHiddenWithDraftsShown(t *testing.T) {
	m := testModel(t)
	data := append([]github.Item(nil), m.allItems...)
	data[0].State, data[0].Draft = "closed", true
	next, _ := m.Update(loaded{result: github.Result{Items: data}})
	m = next.(Model)
	m, _ = press(m, "d")
	if len(m.items) != 1 || m.items[0].Number != 2 {
		t.Fatal("closed item became visible")
	}
}

func TestRefreshSpinner(t *testing.T) {
	m := testModel(t)
	m.spinning = false
	if strings.Contains(m.View(), "refreshing") {
		t.Fatal("idle view shows a spinner")
	}
	next, _ := m.Update(tick{})
	m = next.(Model)
	if !m.loading || !m.spinning {
		t.Fatal("refresh did not start the spinner")
	}
	first := m.View()
	if !strings.Contains(first, spinnerFrames[0]+" refreshing") {
		t.Fatal("refreshing status is missing its spinner")
	}
	next, cmd := m.Update(spinnerTick{})
	m = next.(Model)
	if cmd == nil || m.View() == first || !strings.Contains(m.View(), spinnerFrames[1]+" refreshing") {
		t.Fatal("spinner did not advance while refreshing")
	}
	m.loading = false
	next, cmd = m.Update(spinnerTick{})
	m = next.(Model)
	if cmd != nil || m.spinning {
		t.Fatal("spinner kept running after refresh")
	}
	spin := m.startLoading()
	if spin == nil || m.startLoading() != nil {
		t.Fatal("spinner loop should start exactly once per refresh")
	}
}
