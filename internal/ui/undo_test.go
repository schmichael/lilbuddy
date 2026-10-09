package ui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lilbuddy/internal/github"
	"lilbuddy/internal/state"
)

func TestUndoTriage(t *testing.T) {
	for _, key := range []string{"e", "m", "o"} {
		t.Run(key, func(t *testing.T) {
			m := testModel(t)
			item := m.items[0]
			m.open = func(context.Context, string) error { return nil }
			m, cmd := press(m, key)
			if cmd != nil {
				next, _ := m.Update(cmd())
				m = next.(Model)
			}
			m, cmd = press(m, "u")
			if cmd != nil || len(m.items) != 2 || m.items[m.cursor].ID != item.ID || len(m.store.Items) != 0 || len(m.store.History) != 0 || m.err != nil {
				t.Fatal("undo did not restore and select dismissed item")
			}
			m, _ = press(m, "u")
			if len(m.items) != 2 || m.status != "Nothing to undo" {
				t.Fatal("empty undo changed inbox")
			}
		})
	}
}

func TestUndoAfterRestartAndRefresh(t *testing.T) {
	store, path := cachedStore(t)
	m := NewStartup(context.Background(), store, 0, nil)
	m, _ = press(m, "m")
	// A later successful refresh can replace the entire cache; history must
	// still retain the dismissed item's display snapshot.
	next, _ := m.Update(loaded{result: github.Result{}, revision: m.revision})
	m = next.(Model)
	store, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m = NewStartup(context.Background(), store, 0, nil)
	m, _ = press(m, "u")
	if len(m.items) != 1 || m.items[0].ID != 101 || m.cursor != 0 {
		t.Fatal("restart lost undo snapshot")
	}
	store, err = state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewStartup(context.Background(), store, 0, nil)
	if len(restarted.items) != 1 || len(store.Items) != 0 || len(store.History) != 0 {
		t.Fatal("undo was not persisted with cache restoration")
	}
}

func TestUndoInvalidatesInFlightRefresh(t *testing.T) {
	m := testModel(t)
	m, _ = press(m, "e")
	revision := m.revision
	m.loading = true
	m, _ = press(m, "u")
	next, cmd := m.Update(loaded{result: github.Result{Items: m.items[1:]}, revision: revision})
	m = next.(Model)
	if len(m.items) != 2 || cmd == nil || !m.loading {
		t.Fatal("stale refresh discarded undone item")
	}
}

func TestUndoDraftRespectsVisibility(t *testing.T) {
	m := testModel(t)
	m.allItems[0].Draft = true
	m.showDrafts = true
	m.updateVisible()
	m, _ = press(m, "m")
	m, _ = press(m, "d")
	m, _ = press(m, "u")
	if len(m.items) != 1 || len(m.allItems) != 2 || m.showDrafts {
		t.Fatal("undo bypassed hidden drafts")
	}
	m, _ = press(m, "d")
	if len(m.items) != 2 {
		t.Fatal("undo did not retain hidden draft")
	}
}

func TestUndoKeepsNewerItemAndSorts(t *testing.T) {
	m := testModel(t)
	m.allItems[0].UpdatedAt = time.Now().Add(time.Hour)
	m.updateVisible()
	item := m.items[0]
	m, _ = press(m, "e")
	m, _ = press(m, "u")
	if m.items[0].ID != item.ID {
		t.Fatal("undo broke most-recent-first ordering")
	}
	m, _ = press(m, "e")
	item.Title, item.UpdatedAt = "Newer title", item.UpdatedAt.Add(time.Hour)
	next, _ := m.Update(loaded{result: github.Result{Items: []github.Item{item}}, revision: m.revision})
	m = next.(Model)
	m, _ = press(m, "u")
	if len(m.items) != 1 || m.items[0].Title != "Newer title" {
		t.Fatal("undo duplicated or replaced a newer item")
	}
}

func TestUndoWriteFailureKeepsInboxAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	// Load a copy with an unwritable target (a directory), retaining history.
	store, source := cachedStore(t)
	n := NewStartup(context.Background(), store, 0, nil)
	n, _ = press(n, "m")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	store, err = state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	n = NewStartup(context.Background(), store, 0, nil)
	revision := n.revision
	n, _ = press(n, "u")
	if n.err == nil || len(n.items) != 0 || len(store.History) != 1 || store.Items["me:101"].Action != state.Muted || n.revision != revision {
		t.Fatal("failed undo changed inbox or history")
	}
}
