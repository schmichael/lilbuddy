package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lilbuddy/internal/github"
	"lilbuddy/internal/state"
)

func cachedStore(t *testing.T) (*state.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	err = store.SaveInbox("me", state.Inbox{FetchedAt: when, Items: []state.CachedItem{
		{ID: 101, Repo: "acme/widgets", Number: 1, Title: "Cached issue", State: "open", Kind: "issue", UpdatedAt: when},
		{ID: 102, Repo: "acme/widgets", Number: 2, Title: "Cached draft", State: "open", Kind: "PR", Draft: true, UpdatedAt: when},
		{ID: 103, Repo: "acme/widgets", Number: 3, Title: "Closed issue", State: "closed", Kind: "issue", UpdatedAt: when},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store, err = state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestStartupShowsCacheBeforeAuthentication(t *testing.T) {
	store, _ := cachedStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	connect := func(ctx context.Context) (*github.Client, string, error) {
		close(started)
		select {
		case <-release:
			return nil, "", errors.New("offline")
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewStartup(ctx, store, 0, connect)
	select {
	case <-started:
		t.Fatal("startup blocked on authentication")
	default:
	}
	if len(m.items) != 1 || m.items[0].Title != "Cached issue" || !m.loading || !m.cached || m.authenticated {
		t.Fatal("cache was not displayed immediately with drafts/closed items hidden")
	}
	if !strings.Contains(m.View(), "Cached issue") || !strings.Contains(m.View(), "cached") {
		t.Fatal("cached display was not labeled")
	}
	done := make(chan loaded, 1)
	cmd := m.load()
	go func() { done <- cmd().(loaded) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background authentication did not start")
	}
	m, _ = press(m, "d")
	if len(m.items) != 2 {
		t.Fatal("draft toggle was blocked by background work")
	}
	close(release)
	select {
	case msg := <-done:
		next, _ := m.Update(msg)
		m = next.(Model)
	case <-time.After(2 * time.Second):
		t.Fatal("background authentication did not finish")
	}
	if len(m.items) != 2 || m.err == nil || m.loading || !m.cached {
		t.Fatal("failed refresh discarded the cached display")
	}
}

func TestRefreshPersistsNextStartupDisplayAndSelection(t *testing.T) {
	store, path := cachedStore(t)
	m := NewStartup(context.Background(), store, 0, nil)
	when := time.Now()
	fresh := []github.Item{
		{ID: 104, Repo: "acme/widgets", Number: 4, Title: "New issue", Kind: "issue", State: "open", UpdatedAt: when},
		{ID: 101, Repo: "acme/widgets", Number: 1, Title: "Updated issue", Kind: "issue", State: "open", UpdatedAt: when},
		{ID: 102, Repo: "acme/widgets", Number: 2, Title: "Updated draft", Kind: "PR", State: "open", Draft: true, UpdatedAt: when},
	}
	next, _ := m.Update(loaded{result: github.Result{Items: fresh}})
	m = next.(Model)
	if m.cached || m.loading || len(m.items) != 2 || m.items[m.cursor].ID != 101 {
		t.Fatal("refresh did not replace cache while preserving selection")
	}
	reloaded, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewStartup(context.Background(), reloaded, 0, nil)
	if len(restarted.items) != 2 || restarted.items[0].Title != "New issue" || !restarted.cached || !restarted.updated.Equal(m.updated) {
		t.Fatal("successful refresh was not persisted for next startup")
	}
	restarted, _ = press(restarted, "d")
	if len(restarted.items) != 3 {
		t.Fatal("cache failed to preserve hidden drafts")
	}
}

func TestCachedTriagePersistsDuringRefresh(t *testing.T) {
	for _, action := range []string{"e", "m", "o"} {
		t.Run(action, func(t *testing.T) {
			store, path := cachedStore(t)
			m := NewStartup(context.Background(), store, 0, nil)
			old := append([]github.Item(nil), m.allItems...)
			if action == "o" {
				next, _ := m.Update(opened{item: m.items[0], viewer: "me"})
				m = next.(Model)
			} else {
				m, _ = press(m, action)
			}
			if len(m.items) != 0 {
				t.Fatal("cached triage didn't remove selection")
			}
			reloaded, err := state.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			restarted := NewStartup(context.Background(), reloaded, 0, nil)
			if len(restarted.items) != 0 {
				t.Fatal("restart restored an item dismissed during background refresh")
			}
			next, cmd := m.Update(loaded{result: github.Result{Items: old}, revision: 0})
			if len(next.(Model).items) != 0 || cmd == nil {
				t.Fatal("in-flight refresh undid cached triage")
			}
		})
	}
}

func TestAuthenticationSwitchesCachedAccount(t *testing.T) {
	store, _ := cachedStore(t)
	m := NewStartup(context.Background(), store, 0, nil)
	client := testModel(t).client
	next, _ := m.Update(loaded{client: client, viewer: "other", err: errors.New("discovery failed")})
	m = next.(Model)
	if m.viewer != "other" || len(m.items) != 0 || !m.authenticated {
		t.Fatal("failed discovery displayed a different account's cache")
	}
	// A browser launch that began in the old cached account must remain scoped
	// to that account, even if authentication finishes before the opener does.
	item := github.Item{ID: 101, Repo: "acme/widgets", Number: 1, State: "open", UpdatedAt: time.Now()}
	m.allItems = []github.Item{item}
	m.updateVisible()
	next, _ = m.Update(opened{item: item, viewer: "me"})
	m = next.(Model)
	if len(m.items) != 1 || store.Items[item.StateKey("me")].Action != state.Opened || store.Items[item.StateKey("other")].Action != "" {
		t.Fatal("pending browser launch affected the newly authenticated account")
	}
}

func TestLegacyStateWithoutCacheStillRefreshes(t *testing.T) {
	store, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewStartup(context.Background(), store, 0, nil)
	if m.cached || len(m.items) != 0 || !m.loading {
		t.Fatal("missing cache should leave an empty loading display")
	}
	if !strings.Contains(m.View(), "Finding") {
		t.Fatal("missing cache didn't show loading state")
	}
}
