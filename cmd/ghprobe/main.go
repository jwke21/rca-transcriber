// Command ghprobe is a throwaway manual test harness for the GitHub adapter
// (internal/adapter/github). It is NOT part of any work package and should
// be deleted once you're done testing — do not commit it.
//
// Unlike the Deepgram/Gemini adapters, Publish has real, visible side
// effects: it creates a branch, commits a file and opens (or edits) a pull
// request in whatever repo you point it at. NEVER point this at a real
// project repo — use a disposable scratch repo with a fine-grained PAT
// scoped only to it (Contents + Pull requests, read/write).
//
// Usage:
//
//	go run ./cmd/ghprobe -owner you -repo scratch-repo -token ghp_xxx [-incident 4821] [-base main]
//
// Flags default to the GITHUB_* values in .env / the environment, so if
// you've pointed those at your scratch repo you can omit them.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"

	"github.com/jwke21/rca-transcriber/internal/adapter/github"
	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load(".env") // best-effort; real env vars still win

	token := flag.String("token", os.Getenv("GITHUB_TOKEN"), "GitHub fine-grained PAT, scoped to the scratch repo only")
	owner := flag.String("owner", os.Getenv("GITHUB_OWNER"), "scratch repo owner")
	repo := flag.String("repo", os.Getenv("GITHUB_REPO"), "scratch repo name")
	base := flag.String("base", envOr("GITHUB_BASE_BRANCH", "main"), "base branch")
	incidentID := flag.Int64("incident", 4821, "incident ID; rerun with the same value to exercise the reuse/update paths")
	apiBaseURL := flag.String("api-base-url", "", "override the GitHub API base URL (leave empty for the real api.github.com)")
	flag.Parse()

	if *token == "" || *owner == "" || *repo == "" {
		return fmt.Errorf("-token, -owner and -repo are required (or set GITHUB_TOKEN/GITHUB_OWNER/GITHUB_REPO)")
	}

	fmt.Printf("publishing to %s/%s (base %s), incident %d — make sure this is your scratch repo\n", *owner, *repo, *base, *incidentID)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	publisher, err := github.New(github.Config{
		Token:      *token,
		Owner:      *owner,
		Repo:       *repo,
		BaseBranch: *base,
		APIBaseURL: *apiBaseURL,
	}, logger)
	if err != nil {
		return fmt.Errorf("new publisher: %w", err)
	}

	now := time.Now().UTC()
	doc := domain.RCADocument{
		IncidentID: *incidentID,
		Branch:     domain.RCABranchName(*incidentID),
		Path:       domain.RCAFilePath(*incidentID, now),
		Title:      fmt.Sprintf("Incident %d: ghprobe manual test", *incidentID),
		Body:       "Opened by cmd/ghprobe for manual testing. Safe to close.",
		Content: fmt.Sprintf(
			"# Incident %d\n\n## Date\n\n%s\n\n## Author\n\n%s\n\n## Summary\n\nThis file was written by cmd/ghprobe for manual testing at %s.\n",
			*incidentID, now.Format("2006-01-02"), domain.RCAAuthor, now.Format(time.RFC3339)),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prURL, err := publisher.Publish(ctx, doc)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	fmt.Println("pull request:", prURL)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
