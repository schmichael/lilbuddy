package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/google/go-github/v92/github"

	"lilbuddy/internal/state"
)

// Item represents a PR or Issue that needs attention
type Item struct {
	ID         int64
	Number     int
	Repo       string
	Title      string
	URL        string
	Kind       string
	State      string
	Draft      bool
	draftKnown bool
	UpdatedAt  time.Time
	Reasons    []string
}

func (i Item) Key() string {
	return i.Repo + "#" + strconv.Itoa(i.Number)
}

func (i Item) StateKey(viewer string) string {
	if i.ID != 0 {
		return viewer + ":" + strconv.FormatInt(i.ID, 10)
	}
	return viewer + ":" + i.Key()
}

type Client struct {
	rest  *sdk.Client
	Limit int
}

func New(token string, limit int) (*Client, error) {
	httpClient := &http.Client{Timeout: 60 * time.Second, Transport: readOnlyTransport{base: http.DefaultTransport}}
	rest, err := sdk.NewClient(
		sdk.WithHTTPClient(httpClient),
		sdk.WithAuthToken(token),
		sdk.WithUserAgent("lilbuddy"),
	)
	if err != nil {
		return nil, err
	}
	return &Client{rest: rest, Limit: limit}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, dest any) error {
	req, err := c.rest.NewRequest(ctx, method, strings.TrimPrefix(path, "/"), body)
	if err != nil {
		return err
	}
	_, err = c.rest.Do(req, dest)
	return err
}

func (c *Client) Viewer(ctx context.Context) (string, error) {
	viewer, _, err := c.rest.Users.Get(ctx, "")
	if err != nil {
		return "", err
	}
	return viewer.GetLogin(), nil
}

func issueItem(a *sdk.Issue, reason string) Item {
	kind := "issue"
	if a.IsPullRequest() {
		kind = "PR"
	}
	// Parse the repository from a GitHub API URL, never follow URLs supplied by API data.
	repo := ""
	if u, err := url.Parse(a.GetRepositoryURL()); err == nil {
		repo = strings.TrimPrefix(u.Path, "/repos/")
	}
	return Item{ID: a.GetID(), Number: a.GetNumber(), Repo: repo, Title: a.GetTitle(), URL: a.GetHTMLURL(), Kind: kind, State: a.GetState(), Draft: a.GetDraft(), draftKnown: a.Draft != nil, UpdatedAt: a.GetUpdatedAt().Time, Reasons: []string{reason}}
}

type Result struct {
	Items    []Item
	Warnings []string
}

func (c *Client) Discover(ctx context.Context, viewer string) (Result, error) {
	result := Result{}
	byKey := make(map[string]Item)
	add := func(item Item) {
		if item.State != "open" {
			return
		}
		key := item.Key()
		if old, ok := byKey[key]; ok {
			if old.draftKnown && !item.draftKnown {
				item.Draft, item.draftKnown = old.Draft, true
			}
			if old.UpdatedAt.After(item.UpdatedAt) {
				item.UpdatedAt = old.UpdatedAt
			}
			for _, reason := range old.Reasons {
				if !contains(item.Reasons, reason) {
					item.Reasons = append(item.Reasons, reason)
				}
			}
		}
		byKey[key] = item
	}
	queries := []struct{ query, reason string }{
		{"is:open involves:" + viewer, "involved"},
		{"is:open is:pr reviewed-by:" + viewer, "reviewed"},
		{"is:open is:pr review-requested:" + viewer, "review requested"},
	}
	for _, q := range queries {
		for page, count := 1, 0; count < c.Limit; page++ {
			perPage := min(100, c.Limit-count)
			// Keep page size constant; trim the final page locally.
			response, _, err := c.rest.Search.Issues(ctx, q.query, &sdk.SearchOptions{Sort: "updated", Order: "desc", ListOptions: sdk.ListOptions{PerPage: 100, Page: page}})
			if err != nil {
				return result, fmt.Errorf("search %s: %w", q.reason, err)
			}
			if page == 1 && (response.GetTotal() > c.Limit || response.GetIncompleteResults()) {
				result.Warnings = append(result.Warnings, q.reason+" search is limited or incomplete")
			}
			for _, issue := range response.Issues[:min(perPage, len(response.Issues))] {
				add(issueItem(issue, q.reason))
				count++
			}
			if len(response.Issues) < 100 || count >= response.GetTotal() {
				break
			}
		}
	}
	// Notifications include individually subscribed threads, even if the viewer
	// never commented. Read notifications are intentionally included.
	for page, count := 1, 0; count < c.Limit; page++ {
		threads, _, err := c.rest.Activity.ListNotifications(ctx, &sdk.NotificationListOptions{All: true, ListOptions: sdk.ListOptions{PerPage: 100, Page: page}})
		if err != nil {
			return result, fmt.Errorf("notifications (token needs notifications access): %w", err)
		}
		for _, thread := range threads[:min(c.Limit-count, len(threads))] {
			count++
			if thread.GetSubject().GetType() != "Issue" && thread.GetSubject().GetType() != "PullRequest" {
				continue
			}
			u, err := url.Parse(thread.GetSubject().GetURL())
			if err != nil || u.Host != "api.github.com" || !strings.HasPrefix(u.Path, "/repos/") {
				continue
			}
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) != 5 || (parts[3] != "issues" && parts[3] != "pulls") {
				continue
			}
			number, err := strconv.Atoi(parts[4])
			if err != nil {
				continue
			}
			repo := parts[1] + "/" + parts[2]
			key := fmt.Sprintf("%s#%d", repo, number)
			item, exists := byKey[key]
			if !exists {
				issue, _, err := c.rest.Issues.Get(ctx, parts[1], parts[2], number)
				if err != nil {
					// Deleted or inaccessible threads shouldn't prevent the rest of the inbox loading.
					result.Warnings = append(result.Warnings, "could not load "+key+": "+err.Error())
					continue
				}
				item = issueItem(issue, thread.GetReason())
			}
			if thread.GetUpdatedAt().After(item.UpdatedAt) {
				item.UpdatedAt = thread.GetUpdatedAt().Time
			}
			if !contains(item.Reasons, thread.GetReason()) {
				item.Reasons = append(item.Reasons, thread.GetReason())
			}
			add(item)
		}
		if len(threads) < 100 {
			break
		}
		if count >= c.Limit {
			result.Warnings = append(result.Warnings, "notifications limited to newest "+strconv.Itoa(c.Limit))
		}
	}
	for _, item := range byKey {
		if item.Kind == "PR" && !item.draftKnown {
			parts := strings.Split(item.Repo, "/")
			if len(parts) != 2 {
				result.Warnings = append(result.Warnings, "invalid repository for "+item.Key())
				continue
			}
			pr, _, err := c.rest.PullRequests.Get(ctx, parts[0], parts[1], item.Number)
			if err != nil {
				result.Warnings = append(result.Warnings, "could not check draft status for "+item.Key()+": "+err.Error())
				continue
			}
			if pr.GetState() != "open" {
				continue
			}
			item.Draft, item.draftKnown = pr.GetDraft(), true
		}
		result.Items = append(result.Items, item)
	}
	sortItems(result.Items)
	return result, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func sortItems(items []Item) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].Key() < items[j].Key()
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}

// Filter snapshots state before asynchronous work so the UI can safely record actions.
func (c *Client) Filter(ctx context.Context, result Result, viewer string, entries map[string]state.Entry) Result {
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	visible := make([]Item, 0, len(result.Items))
	warnings := append([]string(nil), result.Warnings...)
	for _, item := range result.Items {
		if item.State != "open" {
			continue
		}
		entry, found := entries[item.StateKey(viewer)]
		if !found {
			mu.Lock()
			visible = append(visible, item)
			mu.Unlock()
			continue
		}
		if entry.Action == state.Muted || !item.UpdatedAt.After(entry.UpdatedAt) {
			continue
		}
		if entry.Action == state.Archived {
			mu.Lock()
			visible = append(visible, item)
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(item Item, since time.Time) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			changed, err := c.ExternalUpdate(ctx, item, viewer, since)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, "could not check "+item.Key()+": "+err.Error())
			} else if changed {
				visible = append(visible, item)
			}
		}(item, entry.UpdatedAt)
	}
	wg.Wait()
	sortItems(visible)
	return Result{Items: visible, Warnings: warnings}
}

// ExternalUpdate examines event actors rather than treating updated_at as proof
// of someone else's activity. It also checks edit attribution through GraphQL.
func (c *Client) ExternalUpdate(ctx context.Context, item Item, viewer string, since time.Time) (bool, error) {
	parts := strings.Split(item.Repo, "/")
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid repository %q", item.Repo)
	}
	for page := 1; ; page++ {
		events, _, err := c.rest.Issues.ListIssueTimeline(ctx, parts[0], parts[1], item.Number, &sdk.ListOptions{PerPage: 100, Page: page})
		if err != nil {
			return false, err
		}
		for _, event := range events {
			when := event.GetCreatedAt().Time
			if event.GetSubmittedAt().After(when) {
				when = event.GetSubmittedAt().Time
			}
			actor := ""
			if event.Actor != nil {
				actor = event.Actor.GetLogin()
			} else if event.User != nil {
				actor = event.User.GetLogin()
			}
			if when.After(since) && actor != "" && !strings.EqualFold(actor, viewer) {
				return true, nil
			}
		}
		if len(events) < 100 {
			break
		}
	}
	changed, err := c.externalEdits(ctx, item, viewer, since)
	if err != nil || changed || item.Kind != "PR" {
		return changed, err
	}
	return c.externalPRActivity(ctx, item, viewer, since)
}

func (c *Client) externalEdits(ctx context.Context, item Item, viewer string, since time.Time) (bool, error) {
	parts := strings.Split(item.Repo, "/")
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid repository %q", item.Repo)
	}
	// GitHub's REST timeline does not expose body edits. GraphQL does, including
	// who last edited a comment (which can be different from its original author).
	const query = `query($owner:String!, $name:String!, $number:Int!, $cursor:String) {
	  repository(owner:$owner,name:$name) {
	    issueOrPullRequest(number:$number) {
	      ... on Issue { lastEditedAt editor { login } comments(first:100,after:$cursor) { nodes { lastEditedAt editor { login } } pageInfo { hasNextPage endCursor } } }
	      ... on PullRequest { lastEditedAt editor { login } comments(first:100,after:$cursor) { nodes { lastEditedAt editor { login } } pageInfo { hasNextPage endCursor } } }
	    }
	  }
	}`
	type edit struct {
		LastEditedAt *time.Time              `json:"lastEditedAt"`
		Editor       *struct{ Login string } `json:"editor"`
	}
	var cursor *string
	for {
		var response struct {
			Data struct {
				Repository *struct {
					Item *struct {
						edit
						Comments struct {
							Nodes    []edit `json:"nodes"`
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"comments"`
					} `json:"issueOrPullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct{ Message string } `json:"errors"`
		}
		body := map[string]any{"query": query, "variables": map[string]any{"owner": parts[0], "name": parts[1], "number": item.Number, "cursor": cursor}}
		if err := c.request(ctx, http.MethodPost, "/graphql", body, &response); err != nil {
			return false, err
		}
		if len(response.Errors) > 0 {
			return false, fmt.Errorf("GraphQL: %s", response.Errors[0].Message)
		}
		if response.Data.Repository == nil || response.Data.Repository.Item == nil {
			return false, fmt.Errorf("item is no longer accessible")
		}
		data := response.Data.Repository.Item
		edits := append(data.Comments.Nodes, data.edit)
		for _, e := range edits {
			if e.LastEditedAt != nil && e.LastEditedAt.After(since) && e.Editor != nil && e.Editor.Login != "" && !strings.EqualFold(e.Editor.Login, viewer) {
				return true, nil
			}
		}
		if !data.Comments.PageInfo.HasNextPage {
			return false, nil
		}
		cursor = &data.Comments.PageInfo.EndCursor
	}
}
