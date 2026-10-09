// Package state persists local triage decisions without changing GitHub.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Action string

const (
	Archived Action = "archived"
	Opened   Action = "opened"
	Muted    Action = "muted"
)

type Entry struct {
	Action    Action    `json:"action"`
	UpdatedAt time.Time `json:"updated_at"`
}

const HistoryLimit = 1000

// Event records enough information to reverse a local triage decision.
type Event struct {
	Key      string      `json:"key"`
	Viewer   string      `json:"viewer"`
	Entry    Entry       `json:"entry"`
	Previous *Entry      `json:"previous,omitempty"`
	Item     *CachedItem `json:"item,omitempty"`
	At       time.Time   `json:"at"`
}

// CachedItem is a display snapshot, not a source of truth about GitHub.
type CachedItem struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Repo      string    `json:"repo"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	UpdatedAt time.Time `json:"updated_at"`
	Reasons   []string  `json:"reasons"`
}

func (item CachedItem) StateKey(viewer string) string {
	if item.ID != 0 {
		return fmt.Sprintf("%s:%d", viewer, item.ID)
	}
	return fmt.Sprintf("%s:%s#%d", viewer, item.Repo, item.Number)
}

type Inbox struct {
	FetchedAt time.Time    `json:"fetched_at"`
	Items     []CachedItem `json:"items"`
	Warnings  []string     `json:"warnings,omitempty"`
}

type Store struct {
	Version    int              `json:"version"`
	Items      map[string]Entry `json:"items"`
	Inboxes    map[string]Inbox `json:"inboxes,omitempty"`
	LastViewer string           `json:"last_viewer,omitempty"`
	History    []Event          `json:"history,omitempty"`
	path       string
}

func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "lilbuddy", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "lilbuddy", "state.json"), nil
}

func Load(path string) (*Store, error) {
	s := &Store{Version: 1, Items: make(map[string]Entry), Inboxes: make(map[string]Inbox), path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if s.Version != 1 {
		return nil, fmt.Errorf("unsupported state version %d", s.Version)
	}
	if s.Items == nil {
		s.Items = make(map[string]Entry)
	}
	if s.Inboxes == nil {
		s.Inboxes = make(map[string]Inbox)
	}
	for key, entry := range s.Items {
		if entry.Action != Archived && entry.Action != Opened && entry.Action != Muted {
			return nil, fmt.Errorf("invalid action %q for %s", entry.Action, key)
		}
	}
	for _, event := range s.History {
		if event.Key == "" || !validAction(event.Entry.Action) || (event.Previous != nil && !validAction(event.Previous.Action)) {
			return nil, fmt.Errorf("invalid history event for %q", event.Key)
		}
		if event.Item != nil && event.Item.StateKey(event.Viewer) != event.Key {
			return nil, fmt.Errorf("history item does not match %q", event.Key)
		}
	}
	if len(s.History) > HistoryLimit {
		s.History = append([]Event(nil), s.History[len(s.History)-HistoryLimit:]...)
	}
	return s, nil
}

func validAction(action Action) bool {
	return action == Archived || action == Opened || action == Muted
}

// Set commits atomically and restores the in-memory value on failure.
func (s *Store) Set(key string, entry Entry) error {
	viewer, _, _ := strings.Cut(key, ":")
	return s.set(key, viewer, nil, entry)
}

// SetItem preserves a display snapshot even if there is no saved inbox yet.
func (s *Store) SetItem(viewer string, item CachedItem, entry Entry) error {
	item.Reasons = append([]string(nil), item.Reasons...)
	return s.set(item.StateKey(viewer), viewer, &item, entry)
}

func (s *Store) set(key, viewer string, item *CachedItem, entry Entry) error {
	if !validAction(entry.Action) {
		return fmt.Errorf("invalid action %q", entry.Action)
	}
	previous, exists := s.Items[key]
	previousInboxes := s.Inboxes
	previousHistory := s.History
	event := Event{Key: key, Viewer: viewer, Entry: entry, Item: item, At: time.Now()}
	if exists {
		event.Previous = &previous
	}
	// Commit the decision and its removal from cached displays together, so a
	// restart during a background refresh cannot restore a dismissed item.
	s.Inboxes = make(map[string]Inbox, len(previousInboxes))
	for viewer, inbox := range previousInboxes {
		items := make([]CachedItem, 0, len(inbox.Items))
		for _, item := range inbox.Items {
			itemKey := item.StateKey(viewer)
			if itemKey != key {
				items = append(items, item)
			} else if event.Item == nil {
				copy := item
				copy.Reasons = append([]string(nil), item.Reasons...)
				event.Item = &copy
			}
		}
		inbox.Items = items
		s.Inboxes[viewer] = inbox
	}
	s.Items[key] = entry
	start := max(0, len(previousHistory)+1-HistoryLimit)
	s.History = append(append([]Event(nil), previousHistory[start:]...), event)
	if err := s.save(); err != nil {
		s.Inboxes = previousInboxes
		s.History = previousHistory
		if exists {
			s.Items[key] = previous
		} else {
			delete(s.Items, key)
		}
		return err
	}
	return nil
}

// Undo reverses the newest action for viewer. A failed write changes nothing.
// Cache refreshes and undo itself are not recorded as new triage actions.
func (s *Store) Undo(viewer string) (Event, bool, error) {
	index := -1
	for i := len(s.History) - 1; i >= 0; i-- {
		if s.History[i].Viewer == viewer {
			index = i
			break
		}
	}
	if index < 0 {
		return Event{}, false, nil
	}
	event := s.History[index]
	previous, exists := s.Items[event.Key]
	previousHistory, previousInboxes := s.History, s.Inboxes
	if event.Previous == nil {
		delete(s.Items, event.Key)
	} else {
		s.Items[event.Key] = *event.Previous
	}
	s.History = make([]Event, 0, len(previousHistory)-1)
	s.History = append(s.History, previousHistory[:index]...)
	s.History = append(s.History, previousHistory[index+1:]...)
	if event.RestoresItem() {
		s.Inboxes = make(map[string]Inbox, len(previousInboxes)+1)
		for account, inbox := range previousInboxes {
			s.Inboxes[account] = inbox
		}
		inbox := s.Inboxes[viewer]
		found := false
		for _, item := range inbox.Items {
			if item.StateKey(viewer) == event.Key {
				found = true
				break
			}
		}
		if !found {
			inbox.Items = append(append([]CachedItem(nil), inbox.Items...), *event.Item)
			sort.SliceStable(inbox.Items, func(i, j int) bool {
				return inbox.Items[i].UpdatedAt.After(inbox.Items[j].UpdatedAt)
			})
		}
		s.Inboxes[viewer] = inbox
	}
	if err := s.save(); err != nil {
		s.History, s.Inboxes = previousHistory, previousInboxes
		if exists {
			s.Items[event.Key] = previous
		} else {
			delete(s.Items, event.Key)
		}
		return Event{}, false, err
	}
	return event, true, nil
}

// RestoresItem reports whether the prior decision permits this snapshot to show.
func (event Event) RestoresItem() bool {
	return event.Item != nil && (event.Previous == nil ||
		(event.Previous.Action != Muted && event.Item.UpdatedAt.After(event.Previous.UpdatedAt)))
}

// SaveInbox also records which account to display before startup authentication.
// Existing version-1 files without an inbox are upgraded on the first refresh.
func (s *Store) SaveInbox(viewer string, inbox Inbox) error {
	previous, exists := s.Inboxes[viewer]
	previousViewer := s.LastViewer
	s.Inboxes[viewer], s.LastViewer = inbox, viewer
	if err := s.save(); err != nil {
		if exists {
			s.Inboxes[viewer] = previous
		} else {
			delete(s.Inboxes, viewer)
		}
		s.LastViewer = previousViewer
		return err
	}
	return nil
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}
