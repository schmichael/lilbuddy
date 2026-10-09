package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func connection(nodes []any, more bool, cursor string) map[string]any {
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor}}
}

func TestPRActivityAttribution(t *testing.T) {
	for _, kind := range []string{"review edit", "inline comment", "inline edit", "commit"} {
		for _, login := range []string{"me", "other", ""} {
			t.Run(kind+"/"+login, func(t *testing.T) {
				c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					pr := map[string]any{"reviews": connection([]any{}, false, ""), "reviewThreads": connection([]any{}, false, ""), "commits": connection([]any{}, false, "")}
					a := map[string]any{"login": login}
					updated := baseline.Add(time.Minute)
					switch kind {
					case "review edit":
						pr["reviews"] = connection([]any{map[string]any{"lastEditedAt": updated, "editor": a}}, false, "")
					case "inline comment", "inline edit":
						comment := map[string]any{"createdAt": updated, "author": a}
						if kind == "inline edit" {
							comment = map[string]any{"createdAt": baseline, "author": map[string]any{"login": "other"}, "lastEditedAt": updated, "editor": a}
						}
						pr["reviewThreads"] = connection([]any{map[string]any{"id": "thread-1", "comments": connection([]any{comment}, false, "")}}, false, "")
					case "commit":
						pr["commits"] = connection([]any{map[string]any{"commit": map[string]any{"committedDate": updated, "committer": map[string]any{"user": a}}}}, false, "")
					}
					writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
				})
				changed, err := c.externalPRActivity(context.Background(), Item{Repo: "acme/widgets", Number: 1}, "me", baseline)
				if err != nil || changed != (login == "other") {
					t.Fatalf("changed = %v, err = %v", changed, err)
				}
			})
		}
	}
}

func TestPRConnectionsPaginateIndependently(t *testing.T) {
	requests := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		pr := map[string]any{"reviews": connection([]any{}, false, "review-end"), "reviewThreads": connection([]any{}, false, "thread-end")}
		if requests == 1 {
			pr["commits"] = connection([]any{}, true, "commit-next")
		} else {
			if body.Variables["reviews"] != "review-end" || body.Variables["threads"] != "thread-end" || body.Variables["commits"] != "commit-next" {
				t.Errorf("incorrect pagination cursors: %v", body.Variables)
			}
			pr["commits"] = connection([]any{map[string]any{"commit": map[string]any{"committedDate": baseline.Add(time.Minute), "committer": map[string]any{"user": map[string]any{"login": "other"}}}}}, false, "commit-end")
		}
		writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
	})
	changed, err := c.externalPRActivity(context.Background(), Item{Repo: "acme/widgets", Number: 1}, "me", baseline)
	if err != nil || !changed || requests != 2 {
		t.Fatalf("changed = %v, requests = %d, err = %v", changed, requests, err)
	}
}

func TestLongInlineDiscussion(t *testing.T) {
	requests := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Query, "node(id:$id)") {
			if body.Variables["id"] != "thread-1" || body.Variables["cursor"] != "comment-next" {
				t.Error("incorrect inline comment pagination")
			}
			writeJSON(w, map[string]any{"data": map[string]any{"node": map[string]any{"comments": connection([]any{map[string]any{"createdAt": baseline.Add(time.Minute), "author": map[string]any{"login": "other"}}}, false, "")}}})
			return
		}
		pr := map[string]any{
			"reviews":       connection([]any{}, false, ""),
			"commits":       connection([]any{}, false, ""),
			"reviewThreads": connection([]any{map[string]any{"id": "thread-1", "comments": connection([]any{}, true, "comment-next")}}, false, ""),
		}
		writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
	})
	changed, err := c.externalPRActivity(context.Background(), Item{Repo: "acme/widgets", Number: 1}, "me", baseline)
	if err != nil || !changed || requests != 2 {
		t.Fatalf("changed = %v, requests = %d, err = %v", changed, requests, err)
	}
}

func TestPRGraphQLErrors(t *testing.T) {
	for _, result := range []map[string]any{{"errors": []any{map[string]any{"message": "rate limited"}}}, {"data": map[string]any{"repository": nil}}} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, result) })
		if _, err := c.externalPRActivity(context.Background(), Item{Repo: "acme/widgets", Number: 1}, "me", baseline); err == nil {
			t.Fatal("ignored GraphQL error")
		}
	}
}

func TestStateKeySurvivesRename(t *testing.T) {
	item := Item{ID: 123, Repo: "before/name", Number: 1}
	key := item.StateKey("me")
	item.Repo, item.Number = "after/name", 22
	if item.StateKey("me") != key || item.StateKey("other") == key {
		t.Fatal("state key is not stable and account-specific")
	}
}
