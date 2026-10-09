package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"lilbuddy/internal/github"
	"lilbuddy/internal/state"
	"lilbuddy/internal/ui"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lilbuddy:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultPath, err := state.DefaultPath()
	if err != nil {
		return err
	}
	path := flag.String("state", defaultPath, "path to local JSON state")
	interval := flag.Duration("refresh", 5*time.Minute, "automatic refresh interval (0 disables)")
	limit := flag.Int("limit", 300, "maximum results per discovery source (1–1000)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("lilbuddy", version)
		return nil
	}
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments; use lilbuddy --help")
	}
	if *limit < 1 || *limit > 1000 {
		return errors.New("--limit must be between 1 and 1000 (GitHub's search limit)")
	}
	if *interval < 0 {
		return errors.New("--refresh cannot be negative")
	}
	store, err := state.Load(*path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connect := func(ctx context.Context) (*github.Client, string, error) {
		token, err := authToken(ctx)
		if err != nil {
			return nil, "", err
		}
		client, err := github.New(token, *limit)
		if err != nil {
			return nil, "", err
		}
		viewer, err := client.Viewer(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("authenticate: %w", err)
		}
		return client, viewer, nil
	}
	_, err = tea.NewProgram(ui.NewStartup(ctx, store, *interval, connect), tea.WithAltScreen()).Run()
	return err
}

func authToken(ctx context.Context) (string, error) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return "", errors.New("sign in with `gh auth login --hostname github.com --scopes repo,notifications`, or set GH_TOKEN/GITHUB_TOKEN")
	}
	return strings.TrimSpace(string(output)), nil
}
