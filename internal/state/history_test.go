package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUndoRoundTripAndPriorDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	item := CachedItem{ID: 123, Repo: "acme/widgets", Number: 1, State: "open", UpdatedAt: when}
	if err := s.SaveInbox("me", Inbox{FetchedAt: when, Warnings: []string{"warning"}, Items: []CachedItem{item}}); err != nil {
		t.Fatal(err)
	}
	prior := Entry{Action: Archived, UpdatedAt: when.Add(-time.Hour)}
	if err := s.SetItem("me", item, prior); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem("me", item, Entry{Action: Muted, UpdatedAt: when}); err != nil {
		t.Fatal(err)
	}
	s, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := s.Undo("me")
	if err != nil || !ok || event.Entry.Action != Muted || s.Items["me:123"] != prior || len(s.History) != 1 {
		t.Fatalf("undo did not restore prior decision: %+v, %v, %v", event, ok, err)
	}
	if len(s.Inboxes["me"].Items) != 1 || !s.Inboxes["me"].FetchedAt.Equal(when) || len(s.Inboxes["me"].Warnings) != 1 {
		t.Fatal("undo did not preserve cache metadata and restore the item")
	}
	if _, ok, err := s.Undo("me"); err != nil || !ok {
		t.Fatalf("second undo: %v, %v", ok, err)
	}
	if len(s.Items) != 0 || len(s.History) != 0 || len(s.Inboxes["me"].Items) != 1 {
		t.Fatal("second undo did not remove decision or duplicated item")
	}
	s, err = Load(path)
	if err != nil || len(s.Items) != 0 || len(s.History) != 0 || len(s.Inboxes["me"].Items) != 1 {
		t.Fatalf("undo not persisted: %v", err)
	}
	if _, ok, err := s.Undo("me"); err != nil || ok {
		t.Fatalf("empty undo: %v, %v", ok, err)
	}
}

func TestUndoIsAccountScoped(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	item := CachedItem{ID: 123, State: "open"}
	for _, viewer := range []string{"me", "other", "me", "other"} {
		if err := s.SetItem(viewer, item, Entry{Action: Muted}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if event, ok, err := s.Undo("me"); err != nil || !ok || event.Viewer != "me" {
			t.Fatalf("account undo: %+v, %v, %v", event, ok, err)
		}
	}
	if _, exists := s.Items["me:123"]; exists || s.Items["other:123"].Action != Muted || len(s.History) != 2 {
		t.Fatal("undo affected another account")
	}
}

func TestHistoryLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a full history to test rollover without 1,000 fsyncs.
	for i := 0; i < HistoryLimit; i++ {
		key := fmt.Sprintf("me:%d", i)
		s.Items[key] = Entry{Action: Muted}
		s.History = append(s.History, Event{Key: key, Viewer: "me", Entry: s.Items[key]})
	}
	for i := HistoryLimit; i < HistoryLimit+3; i++ {
		if err := s.Set(fmt.Sprintf("me:%d", i), Entry{Action: Archived}); err != nil {
			t.Fatal(err)
		}
	}
	s, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.History) != HistoryLimit || s.History[0].Key != "me:3" || s.History[HistoryLimit-1].Key != "me:1002" || s.Items["me:0"].Action != Muted {
		t.Fatal("history rollover dropped decisions or retained old events")
	}
	// Loading an oversized history also keeps only its newest events.
	s.History = append([]Event{{Key: "me:old", Viewer: "me", Entry: Entry{Action: Muted}}}, s.History...)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	s, err = Load(path)
	if err != nil || len(s.History) != HistoryLimit || s.History[0].Key != "me:3" {
		t.Fatalf("oversized history load: %v", err)
	}
}

func TestHistoryWriteFailureIsAtomic(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	item := CachedItem{ID: 123, State: "open"}
	if err := s.SaveInbox("me", Inbox{Items: []CachedItem{item}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem("me", item, Entry{Action: Muted}); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s)
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(parent, "state.json")
	if _, ok, err := s.Undo("me"); err == nil || ok {
		t.Fatal("expected undo write failure")
	}
	after, _ := json.Marshal(s)
	if string(after) != string(before) {
		t.Fatal("failed undo changed state, history, or cache")
	}
	if err := s.SetItem("me", item, Entry{Action: Opened}); err == nil {
		t.Fatal("expected triage write failure")
	}
	after, _ = json.Marshal(s)
	if string(after) != string(before) {
		t.Fatal("failed triage changed state, history, or cache")
	}
}

func TestUndoPreservesNewerCacheAndPreviousMute(t *testing.T) {
	for _, previousMute := range []bool{false, true} {
		s, err := Load(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		old := CachedItem{ID: 123, State: "open", Title: "Old"}
		if previousMute {
			s.Items["me:123"] = Entry{Action: Muted}
		}
		if err := s.SetItem("me", old, Entry{Action: Archived}); err != nil {
			t.Fatal(err)
		}
		fresh := old
		fresh.Title, fresh.UpdatedAt = "New", time.Now()
		if !previousMute {
			if err := s.SaveInbox("me", Inbox{Items: []CachedItem{fresh}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok, err := s.Undo("me"); err != nil || !ok {
			t.Fatalf("undo: %v, %v", ok, err)
		}
		if previousMute {
			if s.Items["me:123"].Action != Muted || len(s.Inboxes["me"].Items) != 0 {
				t.Fatal("undo restored an item suppressed by its previous mute")
			}
		} else if !reflect.DeepEqual(s.Inboxes["me"].Items, []CachedItem{fresh}) {
			t.Fatal("undo replaced newer cache or duplicated item")
		}
	}
}

func TestInvalidHistoryNotOverwritten(t *testing.T) {
	for _, history := range []string{
		`[{"key":"me:1","entry":{"action":"unknown"}}]`,
		`[{"key":"me:1","entry":{"action":"muted"},"previous":{"action":"unknown"}}]`,
		`[{"key":"me:1","viewer":"me","entry":{"action":"muted"},"item":{"id":2}}]`,
	} {
		path := filepath.Join(t.TempDir(), "state.json")
		contents := `{"version":1,"history":` + history + `}`
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("accepted invalid history")
		}
		got, _ := os.ReadFile(path)
		if string(got) != contents {
			t.Fatal("invalid history was overwritten")
		}
	}
}
