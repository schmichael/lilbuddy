package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/google/go-github/v92/github"
)

func TestReadOnlyTransport(t *testing.T) {
	var reached atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		writeJSON(w, map[string]any{})
	})
	for _, tc := range []struct {
		name, method, path, query string
		allowed                   bool
	}{
		{"REST get", "GET", "/user", "", true},
		{"REST post", "POST", "/issues", "", false},
		{"REST patch", "PATCH", "/notifications/threads/1", "", false},
		{"REST put", "PUT", "/notifications", "", false},
		{"REST delete", "DELETE", "/notifications/threads/1", "", false},
		{"REST post disguised as query", "POST", "/issues", `query { viewer { login } }`, false},
		{"GraphQL query", "POST", "/graphql", `query Read { viewer { login } }`, true},
		{"GraphQL implicit query", "POST", "/graphql", `{ viewer { login } }`, true},
		{"GraphQL string with mutation keyword", "POST", "/graphql", `query { repository(name:"mutation",owner:"x") { name } }`, true},
		{"GraphQL mutation", "POST", "/graphql", `mutation { markNotificationAsDone(input:{threadId:"1"}) { clientMutationId } }`, false},
		{"GraphQL commented mutation", "POST", "/graphql", "# query\n mutation Write { x }", false},
		{"GraphQL mixed operations", "POST", "/graphql", `query Read { viewer { login } } mutation Write { x }`, false},
		{"GraphQL subscription", "POST", "/graphql", `subscription { x }`, false},
		{"GraphQL empty", "POST", "/graphql", "", false},
		{"GraphQL invalid", "POST", "/graphql", `query {`, false},
		{"GraphQL fragment only", "POST", "/graphql", `fragment f on User { login }`, false},
		{"GraphQL get blocked", "GET", "/graphql?query=mutation%20%7Bx%7D", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := reached.Load()
			var dest any
			err := c.request(context.Background(), tc.method, tc.path, map[string]any{"query": tc.query}, &dest)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed = %v, err = %v", tc.allowed, err)
			}
			if got := reached.Load() - before; (got == 1) != tc.allowed {
				t.Fatalf("request reached API %d times (allowed=%v)", got, tc.allowed)
			}
		})
	}
}

func TestSDKWritesAreBlocked(t *testing.T) {
	var reached atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		writeJSON(w, map[string]any{})
	})
	ctx := context.Background()
	_, _, err := c.rest.Issues.Update(ctx, "acme", "widgets", 1, sdk.UpdateIssueRequest{State: sdk.Ptr("closed")})
	if err == nil || !strings.Contains(err.Error(), "blocks GitHub write") {
		t.Fatalf("SDK issue edit not blocked: %v", err)
	}
	_, err = c.rest.Activity.MarkThreadRead(ctx, "1")
	if err == nil {
		t.Fatal("SDK mark-read request not blocked")
	}
	_, err = c.rest.Activity.MarkThreadDone(ctx, "1")
	if err == nil {
		t.Fatal("SDK mark-done request not blocked")
	}
	if reached.Load() != 0 {
		t.Fatal("SDK write reached the API")
	}
}

func TestGraphQLPayloadPreserved(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Variables["name"] != "widgets" || body.Query != `query($name:String!) { repository(name:$name,owner:"acme") { name } }` {
			t.Errorf("transport modified payload: %+v", body)
		}
		writeJSON(w, map[string]any{})
	})
	var dest any
	err := c.request(context.Background(), http.MethodPost, "/graphql", map[string]any{"query": `query($name:String!) { repository(name:$name,owner:"acme") { name } }`, "variables": map[string]any{"name": "widgets"}}, &dest)
	if err != nil {
		t.Fatal(err)
	}
}
