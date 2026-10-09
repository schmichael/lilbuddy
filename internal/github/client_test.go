package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/google/go-github/v92/github"

	"lilbuddy/internal/state"
)

var baseline = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New("test-token", 300)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := server.URL + "/"
	c.rest, err = c.rest.Clone(sdk.WithURLs(&baseURL, nil))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func issue(number int, when time.Time, pr bool) map[string]any {
	i := map[string]any{"id": number, "number": number, "title": fmt.Sprintf("Item %d", number), "repository_url": "https://api.github.com/repos/acme/widgets", "html_url": fmt.Sprintf("https://github.com/acme/widgets/issues/%d", number), "state": "open", "updated_at": when}
	if pr {
		i["pull_request"] = map[string]any{}
		i["draft"] = false
	}
	return i
}

func TestDiscoverCombinesSources(t *testing.T) {
	seenQueries := make(map[string]bool)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || !strings.HasPrefix(r.Header.Get("Accept"), "application/vnd.github") {
			t.Error("missing API headers")
		}
		switch r.URL.Path {
		case "/user":
			writeJSON(w, map[string]any{"login": "me"})
		case "/search/issues":
			query := r.URL.Query().Get("q")
			seenQueries[query] = true
			writeJSON(w, map[string]any{"total_count": 1, "items": []any{issue(1, baseline, true)}})
		case "/notifications":
			if r.URL.Query().Get("all") != "true" {
				t.Error("must include read notifications")
			}
			writeJSON(w, []any{
				map[string]any{"reason": "subscribed", "updated_at": baseline.Add(time.Hour), "subject": map[string]any{"type": "Issue", "url": "https://api.github.com/repos/acme/widgets/issues/2"}},
				map[string]any{"reason": "mention", "updated_at": baseline.Add(2 * time.Hour), "subject": map[string]any{"type": "PullRequest", "url": "https://api.github.com/repos/acme/widgets/pulls/1"}},
				map[string]any{"subject": map[string]any{"type": "Release", "url": "https://api.github.com/repos/acme/widgets/releases/3"}},
				map[string]any{"subject": map[string]any{"type": "Issue", "url": "https://untrusted.example/repos/acme/widgets/issues/4"}},
			})
		case "/repos/acme/widgets/issues/2":
			writeJSON(w, issue(2, baseline, false))
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	viewer, err := c.Viewer(context.Background())
	if err != nil || viewer != "me" {
		t.Fatalf("viewer = %q, %v", viewer, err)
	}
	got, err := c.Discover(context.Background(), viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].Number != 1 || got.Items[0].Kind != "PR" || got.Items[1].Number != 2 {
		t.Fatalf("bad results: %+v", got)
	}
	if len(got.Items[0].Reasons) != 4 || !contains(got.Items[1].Reasons, "subscribed") {
		t.Fatalf("lost discovery reasons: %+v", got.Items)
	}
	for _, query := range []string{"is:open involves:me", "is:open is:pr reviewed-by:me", "is:open is:pr review-requested:me"} {
		if !seenQueries[query] {
			t.Errorf("missing query %s", query)
		}
	}
}

func TestDiscoverPaginationAndLimit(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications" {
			writeJSON(w, []any{})
			return
		}
		if r.URL.Query().Get("q") != "is:open involves:me" {
			writeJSON(w, map[string]any{"total_count": 0, "items": []any{}})
			return
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("page size must stay constant")
		}
		items := make([]any, 100)
		start := 0
		if r.URL.Query().Get("page") == "2" {
			start = 100
		}
		for i := range items {
			items[i] = issue(start+i+1, baseline, false)
		}
		writeJSON(w, map[string]any{"total_count": 200, "items": items})
	})
	c.Limit = 150
	got, err := c.Discover(context.Background(), "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 150 || len(got.Warnings) != 1 {
		t.Fatalf("limit not enforced: %d items, %v", len(got.Items), got.Warnings)
	}
}

func TestFilterActions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  state.Action
		updated bool
		actor   string
		visible bool
	}{
		{"archive unchanged", state.Archived, false, "", false},
		{"archive self update", state.Archived, true, "me", true},
		{"mute forever", state.Muted, true, "other", false},
		{"opened unchanged", state.Opened, false, "", false},
		{"opened own update", state.Opened, true, "ME", false},
		{"opened external update", state.Opened, true, "other", true},
		{"opened bot update", state.Opened, true, "bot[bot]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/timeline") {
					writeJSON(w, []any{map[string]any{"created_at": baseline.Add(time.Minute), "actor": map[string]any{"login": tc.actor}}})
					return
				}
				writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"issueOrPullRequest": map[string]any{"comments": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}}}}})
			})
			when := baseline
			if tc.updated {
				when = when.Add(time.Minute)
			}
			item := Item{Repo: "acme/widgets", Number: 1, State: "open", UpdatedAt: when}
			entries := map[string]state.Entry{"me:" + item.Key(): {Action: tc.action, UpdatedAt: baseline}}
			got := c.Filter(context.Background(), Result{Items: []Item{item}}, "me", entries)
			if (len(got.Items) == 1) != tc.visible || len(got.Warnings) != 0 {
				t.Fatalf("unexpected filter result: %+v", got)
			}
			// Account-specific decisions must not hide items for a different viewer.
			got = c.Filter(context.Background(), Result{Items: []Item{item}}, "someone-else", entries)
			if len(got.Items) != 1 {
				t.Fatal("state leaked across accounts")
			}
		})
	}
}

func TestExternalEdits(t *testing.T) {
	for _, editor := range []string{"me", "other"} {
		t.Run(editor, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/graphql" {
					writeJSON(w, []any{})
					return
				}
				writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"issueOrPullRequest": map[string]any{"lastEditedAt": baseline.Add(time.Minute), "editor": map[string]any{"login": editor}, "comments": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}}}}})
			})
			changed, err := c.ExternalUpdate(context.Background(), Item{Repo: "acme/widgets", Number: 1}, "me", baseline)
			if err != nil || changed != (editor == "other") {
				t.Fatalf("changed = %v, err = %v", changed, err)
			}
		})
	}
}

func TestExternalCommentEditPagination(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			writeJSON(w, []any{})
			return
		}
		var request struct {
			Variables struct{ Cursor *string } `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		nodes := []any{}
		hasNext := request.Variables.Cursor == nil
		if !hasNext {
			nodes = append(nodes, map[string]any{"lastEditedAt": baseline.Add(time.Minute), "editor": map[string]any{"login": "other"}})
		}
		writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"issueOrPullRequest": map[string]any{"comments": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": "next"}}}}}})
	})
	changed, err := c.ExternalUpdate(context.Background(), Item{Repo: "acme/widgets", Number: 1}, "me", baseline)
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
}

func TestFilterConcurrentAndErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"rate limit exceeded"}`, http.StatusForbidden)
	})
	items := make([]Item, 80)
	entries := make(map[string]state.Entry)
	for i := range items {
		items[i] = Item{Repo: "acme/widgets", Number: i + 1, State: "open", UpdatedAt: baseline.Add(time.Minute)}
		if i%2 == 0 {
			entries["me:"+items[i].Key()] = state.Entry{Action: state.Opened, UpdatedAt: baseline}
		}
	}
	got := c.Filter(context.Background(), Result{Items: items}, "me", entries)
	if len(got.Items) != 40 || len(got.Warnings) != 40 {
		t.Fatalf("bad result: %d visible, %d warnings", len(got.Items), len(got.Warnings))
	}
}

func TestDiscoverOpenOnlyIncludingNotifications(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			if !strings.Contains(r.URL.Query().Get("q"), "is:open") {
				t.Error("search must restrict to open items")
			}
			closedIssue := issue(2, baseline, false)
			closedIssue["state"] = "closed"
			mergedPR := issue(3, baseline, true)
			mergedPR["state"] = "closed"
			writeJSON(w, map[string]any{"total_count": 3, "items": []any{issue(1, baseline, false), closedIssue, mergedPR}})
		case "/notifications":
			writeJSON(w, []any{
				map[string]any{"reason": "subscribed", "subject": map[string]any{"type": "Issue", "url": "https://api.github.com/repos/acme/widgets/issues/4"}},
				map[string]any{"reason": "subscribed", "subject": map[string]any{"type": "PullRequest", "url": "https://api.github.com/repos/acme/widgets/pulls/5"}},
			})
		case "/repos/acme/widgets/issues/4", "/repos/acme/widgets/issues/5":
			closed := issue(4, baseline, false)
			if strings.HasSuffix(r.URL.Path, "/5") {
				closed = issue(5, baseline, true)
			}
			closed["state"] = "closed"
			writeJSON(w, closed)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := c.Discover(context.Background(), "me")
	if err != nil || len(result.Items) != 1 || result.Items[0].Number != 1 {
		t.Fatalf("closed items leaked: %+v, %v", result, err)
	}
}

func TestDraftMetadataFromSearchAndPRDetails(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			draft := issue(1, baseline, true)
			draft["draft"] = true
			writeJSON(w, map[string]any{"total_count": 1, "items": []any{draft}})
		case "/notifications":
			writeJSON(w, []any{map[string]any{"reason": "subscribed", "subject": map[string]any{"type": "PullRequest", "url": "https://api.github.com/repos/acme/widgets/pulls/2"}}})
		case "/repos/acme/widgets/issues/2":
			pr := issue(2, baseline, true)
			delete(pr, "draft") // Issues API may omit PR draft metadata.
			writeJSON(w, pr)
		case "/repos/acme/widgets/pulls/2":
			writeJSON(w, map[string]any{"id": 9999, "state": "open", "draft": true})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := c.Discover(context.Background(), "me")
	if err != nil || len(result.Items) != 2 {
		t.Fatalf("discovery failed: %+v, %v", result, err)
	}
	for _, item := range result.Items {
		if !item.Draft || !item.draftKnown || item.ID != int64(item.Number) {
			t.Fatalf("wrong draft or identity metadata: %+v", item)
		}
	}
}

func TestFilterNeverShowsClosedItems(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("closed items must not trigger activity checks")
	})
	items := []Item{{State: "closed", Repo: "acme/widgets", Number: 1, UpdatedAt: baseline.Add(time.Hour)}}
	result := c.Filter(context.Background(), Result{Items: items}, "me", nil)
	if len(result.Items) != 0 {
		t.Fatal("closed item shown")
	}
}
