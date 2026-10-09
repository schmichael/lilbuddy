package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, action := range []Action{Archived, Opened, Muted} {
		if err := s.Set(string(action), Entry{Action: action, UpdatedAt: when}); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{Archived, Opened, Muted} {
		if got := reloaded.Items[string(action)]; got.Action != action || !got.UpdatedAt.Equal(when) {
			t.Fatalf("bad entry: %+v", got)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions = %o", info.Mode().Perm())
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".state-*"))
	if len(files) != 0 {
		t.Fatalf("temporary files left behind: %v", files)
	}
}

func TestInvalidStateNotOverwritten(t *testing.T) {
	for _, contents := range []string{`{oops`, `{"version":2,"items":{}}`, `{"version":1,"items":{"x":{"action":"unknown"}}}`} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted bad state: %s", contents)
		}
		got, _ := os.ReadFile(path)
		if string(got) != contents {
			t.Fatal("bad state was overwritten")
		}
	}
}

func TestFailedWriteRollsBack(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(parent, "state.json")
	if err := s.Set("new", Entry{Action: Muted}); err == nil {
		t.Fatal("expected write failure")
	}
	if len(s.Items) != 0 {
		t.Fatal("failed action persisted in memory")
	}
	s.Items["existing"] = Entry{Action: Archived}
	if err := s.Set("existing", Entry{Action: Muted}); err == nil {
		t.Fatal("expected write failure")
	}
	if s.Items["existing"].Action != Archived {
		t.Fatal("previous state wasn't restored")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	got, err := DefaultPath()
	if err != nil || got != "/custom/state/lilbuddy/state.json" {
		t.Fatalf("path = %q, %v", got, err)
	}
}

func TestInboxRoundTripAndAtomicDismissal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	inbox := Inbox{FetchedAt: when, Warnings: []string{"partial results"}, Items: []CachedItem{
		{ID: 123, Number: 1, Repo: "acme/widgets", Title: "Cached issue", State: "open", URL: "https://github.com/acme/widgets/issues/1", UpdatedAt: when},
		{ID: 124, Number: 2, Repo: "acme/widgets", Title: "Cached draft", State: "open", Draft: true, UpdatedAt: when},
	}}
	for _, viewer := range []string{"me", "other"} {
		if err := s.SaveInbox(viewer, inbox); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set("me:123", Entry{Action: Archived, UpdatedAt: when}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LastViewer != "other" || len(reloaded.Inboxes["other"].Items) != 2 || len(reloaded.Inboxes["me"].Items) != 1 || !reloaded.Inboxes["me"].Items[0].Draft {
		t.Fatal("cache dismissal was not atomic or account-scoped")
	}
	if !reloaded.Inboxes["me"].FetchedAt.Equal(when) || len(reloaded.Inboxes["me"].Warnings) != 1 || reloaded.Items["me:123"].Action != Archived {
		t.Fatal("cache metadata or action was lost")
	}
}

func TestCacheWriteFailuresRollBack(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	inbox := Inbox{Items: []CachedItem{{ID: 123, Title: "Keep me"}}}
	if err := s.SaveInbox("me", inbox); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(parent, "state.json")
	if err := s.Set("me:123", Entry{Action: Muted}); err == nil {
		t.Fatal("expected write failure")
	}
	if len(s.Items) != 0 || len(s.Inboxes["me"].Items) != 1 {
		t.Fatal("failed triage changed cache or actions")
	}
	if err := s.SaveInbox("other", Inbox{}); err == nil {
		t.Fatal("expected cache write failure")
	}
	if s.LastViewer != "me" || len(s.Inboxes) != 1 {
		t.Fatal("failed cache save changed account")
	}
	if err := s.SaveInbox("me", Inbox{}); err == nil {
		t.Fatal("expected cache replacement failure")
	}
	if len(s.Inboxes["me"].Items) != 1 {
		t.Fatal("failed cache replacement lost previous inbox")
	}
}

func TestLegacyVersionOneWithoutInbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"items":{"me:123":{"action":"muted","updated_at":"2026-10-08T12:00:00Z"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil || s.Inboxes == nil || s.Items["me:123"].Action != Muted {
		t.Fatalf("legacy state failed to load: %v", err)
	}
	if err := s.SaveInbox("me", Inbox{Items: []CachedItem{}}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.LastViewer != "me" || reloaded.Items["me:123"].Action != Muted {
		t.Fatalf("legacy state failed to upgrade: %v", err)
	}
}
