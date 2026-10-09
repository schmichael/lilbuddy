package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type actor struct{ Login string }

type activity struct {
	CreatedAt    time.Time  `json:"createdAt"`
	LastEditedAt *time.Time `json:"lastEditedAt"`
	Author       *actor     `json:"author"`
	Editor       *actor     `json:"editor"`
}

func (a activity) external(viewer string, since time.Time) bool {
	return (a.CreatedAt.After(since) && externalActor(a.Author, viewer)) ||
		(a.LastEditedAt != nil && a.LastEditedAt.After(since) && externalActor(a.Editor, viewer))
}

func externalActor(a *actor, viewer string) bool {
	return a != nil && a.Login != "" && !strings.EqualFold(a.Login, viewer)
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type activityConnection struct {
	Nodes    []activity `json:"nodes"`
	PageInfo pageInfo   `json:"pageInfo"`
}

// Review threads, review edits, and commits aren't all represented by actors in
// the issue timeline. Paginate each connection independently, including long
// inline discussions, instead of only considering the last 100 events.
func (c *Client) externalPRActivity(ctx context.Context, item Item, viewer string, since time.Time) (bool, error) {
	const query = `query($owner:String!, $name:String!, $number:Int!, $reviews:String, $threads:String, $commits:String) {
	  repository(owner:$owner,name:$name) { pullRequest(number:$number) {
	    reviews(first:100,after:$reviews) { nodes { lastEditedAt editor { login } } pageInfo { hasNextPage endCursor } }
	    reviewThreads(first:100,after:$threads) { nodes { id comments(first:100) { nodes { createdAt author { login } lastEditedAt editor { login } } pageInfo { hasNextPage endCursor } } } pageInfo { hasNextPage endCursor } }
	    commits(first:100,after:$commits) { nodes { commit { committedDate committer { user { login } } } } pageInfo { hasNextPage endCursor } }
	  } }
	}`
	parts := strings.Split(item.Repo, "/")
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid repository %q", item.Repo)
	}
	variables := map[string]any{"owner": parts[0], "name": parts[1], "number": item.Number}
	for {
		var response struct {
			Data struct {
				Repository *struct {
					PR *struct {
						Reviews activityConnection `json:"reviews"`
						Threads struct {
							Nodes []struct {
								ID       string             `json:"id"`
								Comments activityConnection `json:"comments"`
							} `json:"nodes"`
							PageInfo pageInfo `json:"pageInfo"`
						} `json:"reviewThreads"`
						Commits struct {
							Nodes []struct {
								Commit struct {
									CommittedDate time.Time              `json:"committedDate"`
									Committer     *struct{ User *actor } `json:"committer"`
								} `json:"commit"`
							} `json:"nodes"`
							PageInfo pageInfo `json:"pageInfo"`
						} `json:"commits"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct{ Message string } `json:"errors"`
		}
		if err := c.request(ctx, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": variables}, &response); err != nil {
			return false, err
		}
		if len(response.Errors) > 0 {
			return false, fmt.Errorf("GraphQL: %s", response.Errors[0].Message)
		}
		if response.Data.Repository == nil || response.Data.Repository.PR == nil {
			return false, fmt.Errorf("pull request is no longer accessible")
		}
		pr := response.Data.Repository.PR
		for _, review := range pr.Reviews.Nodes {
			if review.external(viewer, since) {
				return true, nil
			}
		}
		for _, thread := range pr.Threads.Nodes {
			for _, comment := range thread.Comments.Nodes {
				if comment.external(viewer, since) {
					return true, nil
				}
			}
			if thread.Comments.PageInfo.HasNextPage {
				changed, err := c.externalThreadComments(ctx, thread.ID, thread.Comments.PageInfo.EndCursor, viewer, since)
				if changed || err != nil {
					return changed, err
				}
			}
		}
		for _, node := range pr.Commits.Nodes {
			commit := node.Commit
			if commit.CommittedDate.After(since) && commit.Committer != nil && externalActor(commit.Committer.User, viewer) {
				return true, nil
			}
		}
		if !pr.Reviews.PageInfo.HasNextPage && !pr.Threads.PageInfo.HasNextPage && !pr.Commits.PageInfo.HasNextPage {
			return false, nil
		}
		for name, page := range map[string]pageInfo{"reviews": pr.Reviews.PageInfo, "threads": pr.Threads.PageInfo, "commits": pr.Commits.PageInfo} {
			// Keep exhausted connections at their end instead of restarting them.
			if page.EndCursor != "" {
				variables[name] = page.EndCursor
			}
		}
	}
}

func (c *Client) externalThreadComments(ctx context.Context, id, cursor, viewer string, since time.Time) (bool, error) {
	const query = `query($id:ID!, $cursor:String!) { node(id:$id) { ... on PullRequestReviewThread { comments(first:100,after:$cursor) { nodes { createdAt author { login } lastEditedAt editor { login } } pageInfo { hasNextPage endCursor } } } } }`
	for {
		var response struct {
			Data struct {
				Node *struct{ Comments activityConnection } `json:"node"`
			} `json:"data"`
			Errors []struct{ Message string } `json:"errors"`
		}
		body := map[string]any{"query": query, "variables": map[string]any{"id": id, "cursor": cursor}}
		if err := c.request(ctx, http.MethodPost, "/graphql", body, &response); err != nil {
			return false, err
		}
		if len(response.Errors) > 0 {
			return false, fmt.Errorf("GraphQL: %s", response.Errors[0].Message)
		}
		if response.Data.Node == nil {
			return false, fmt.Errorf("review thread is no longer accessible")
		}
		comments := response.Data.Node.Comments
		for _, comment := range comments.Nodes {
			if comment.external(viewer, since) {
				return true, nil
			}
		}
		if !comments.PageInfo.HasNextPage {
			return false, nil
		}
		cursor = comments.PageInfo.EndCursor
	}
}
