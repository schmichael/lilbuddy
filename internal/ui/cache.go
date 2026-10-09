package ui

import (
	"time"

	"lilbuddy/internal/github"
	"lilbuddy/internal/state"
)

func (m *Model) restoreCache() {
	m.allItems = nil
	m.warnings = nil
	m.cached = false
	m.updated = time.Time{}
	if inbox, ok := m.store.Inboxes[m.viewer]; ok {
		m.cached = true
		m.updated = inbox.FetchedAt
		m.warnings = append([]string(nil), inbox.Warnings...)
		for _, item := range inbox.Items {
			// Respect local decisions even if the cache was manually edited.
			candidate := fromCache(item)
			if entry, ok := m.store.Items[candidate.StateKey(m.viewer)]; ok && (entry.Action == state.Muted || !candidate.UpdatedAt.After(entry.UpdatedAt)) {
				continue
			}
			m.allItems = append(m.allItems, candidate)
		}
	}
	m.updateVisible()
}

func (m Model) snapshot() state.Inbox {
	inbox := state.Inbox{FetchedAt: m.updated, Items: make([]state.CachedItem, 0, len(m.allItems)), Warnings: append([]string(nil), m.warnings...)}
	for _, item := range m.allItems {
		inbox.Items = append(inbox.Items, toCache(item))
	}
	return inbox
}

func toCache(item github.Item) state.CachedItem {
	return state.CachedItem{ID: item.ID, Number: item.Number, Repo: item.Repo, Title: item.Title, URL: item.URL, Kind: item.Kind, State: item.State, Draft: item.Draft, UpdatedAt: item.UpdatedAt, Reasons: append([]string(nil), item.Reasons...)}
}

func fromCache(item state.CachedItem) github.Item {
	return github.Item{ID: item.ID, Number: item.Number, Repo: item.Repo, Title: item.Title, URL: item.URL, Kind: item.Kind, State: item.State, Draft: item.Draft, UpdatedAt: item.UpdatedAt, Reasons: append([]string(nil), item.Reasons...)}
}
