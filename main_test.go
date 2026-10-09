package main

import (
	"context"
	"testing"
)

func TestAuthTokenEnvironmentPrecedence(t *testing.T) {
	t.Setenv("GH_TOKEN", " first \n")
	t.Setenv("GITHUB_TOKEN", "second")
	token, err := authToken(context.Background())
	if err != nil || token != "first" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	t.Setenv("GH_TOKEN", "")
	token, err = authToken(context.Background())
	if err != nil || token != "second" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}
