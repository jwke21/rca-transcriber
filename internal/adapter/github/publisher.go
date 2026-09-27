// Package github implements port.RCAPublisher on top of the GitHub REST API.
// It commits the RCA Markdown file to the incident's branch and opens (or
// updates) a pull request, which is the human review step for AI-written
// content (IMPLEMENTATION.md §1 FR5, §6.3).
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v66/github"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// httpTimeout bounds every GitHub API request.
const httpTimeout = 30 * time.Second

// Config configures a Publisher.
type Config struct {
	// Token is a GitHub token with Contents and Pull requests read/write access.
	// Never logged or included in errors.
	Token string
	// Owner is the repository owner (user or organisation).
	Owner string
	// Repo is the repository name.
	Repo string
	// BaseBranch is the branch pull requests target, e.g. "main".
	BaseBranch string
	// APIBaseURL overrides the GitHub API URL. Tests only; empty means api.github.com.
	APIBaseURL string
}

// Publisher implements port.RCAPublisher using the GitHub REST API.
type Publisher struct {
	client *gh.Client
	owner  string
	repo   string
	base   string
	logger *slog.Logger
}

var _ port.RCAPublisher = (*Publisher)(nil)

// New creates a Publisher. Token, Owner, Repo and BaseBranch are required.
// logger may be nil, in which case logs are discarded.
func New(cfg Config, logger *slog.Logger) (*Publisher, error) {
	switch {
	case cfg.Token == "":
		return nil, errors.New("github: token is required")
	case cfg.Owner == "":
		return nil, errors.New("github: owner is required")
	case cfg.Repo == "":
		return nil, errors.New("github: repo is required")
	case cfg.BaseBranch == "":
		return nil, errors.New("github: base branch is required")
	}

	client := gh.NewClient(&http.Client{Timeout: httpTimeout}).WithAuthToken(cfg.Token)
	if cfg.APIBaseURL != "" {
		base, err := url.Parse(strings.TrimSuffix(cfg.APIBaseURL, "/") + "/")
		if err != nil {
			return nil, fmt.Errorf("github: parse api base url: %w", err)
		}
		client.BaseURL = base
	}

	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Publisher{
		client: client,
		owner:  cfg.Owner,
		repo:   cfg.Repo,
		base:   cfg.BaseBranch,
		logger: logger,
	}, nil
}

// Publish commits doc.Content to doc.Path on doc.Branch and opens a pull
// request, or updates the existing branch, file and open pull request.
// It follows IMPLEMENTATION.md §6.3 and returns the pull request's HTML URL.
func (p *Publisher) Publish(ctx context.Context, doc domain.RCADocument) (string, error) {
	id := strconv.FormatInt(doc.IncidentID, 10)

	// Step 1: base branch SHA.
	baseRef, _, err := p.client.Git.GetRef(ctx, p.owner, p.repo, "heads/"+p.base)
	if err != nil {
		if isStatus(err, http.StatusNotFound) {
			return "", fmt.Errorf("github: get base branch %s: %w: %w", p.base, domain.ErrNotFound, err)
		}
		return "", fmt.Errorf("github: get base branch %s: %w", p.base, err)
	}
	baseSHA := baseRef.GetObject().GetSHA()
	if baseSHA == "" {
		return "", fmt.Errorf("github: get base branch %s: response has no commit sha", p.base)
	}

	// Step 2: create the incident branch; 422 means it already exists.
	_, _, err = p.client.Git.CreateRef(ctx, p.owner, p.repo, &gh.Reference{
		Ref:    gh.String("refs/heads/" + doc.Branch),
		Object: &gh.GitObject{SHA: gh.String(baseSHA)},
	})
	switch {
	case err == nil:
		p.logger.DebugContext(ctx, "github: created branch", slog.String("branch", doc.Branch))
	case isStatus(err, http.StatusUnprocessableEntity):
		p.logger.DebugContext(ctx, "github: branch already exists, reusing it", slog.String("branch", doc.Branch))
	default:
		return "", fmt.Errorf("github: create branch %s: %w", doc.Branch, err)
	}

	// Step 3: look for an existing file on the branch; 404 means create.
	var existingSHA string
	file, _, _, err := p.client.Repositories.GetContents(ctx, p.owner, p.repo, doc.Path,
		&gh.RepositoryContentGetOptions{Ref: doc.Branch})
	switch {
	case err == nil:
		if file == nil {
			return "", fmt.Errorf("github: get file %s on %s: path is a directory", doc.Path, doc.Branch)
		}
		existingSHA = file.GetSHA()
	case isStatus(err, http.StatusNotFound):
		// Not there yet: create it.
	default:
		return "", fmt.Errorf("github: get file %s on %s: %w", doc.Path, doc.Branch, err)
	}

	// Step 4: create or update the file.
	opts := &gh.RepositoryContentFileOptions{
		Content: []byte(doc.Content),
		Branch:  gh.String(doc.Branch),
	}
	if existingSHA == "" {
		opts.Message = gh.String("Add RCA for incident " + id)
		if _, _, err := p.client.Repositories.CreateFile(ctx, p.owner, p.repo, doc.Path, opts); err != nil {
			return "", fmt.Errorf("github: create file %s on %s: %w", doc.Path, doc.Branch, err)
		}
	} else {
		opts.Message = gh.String("Update RCA for incident " + id)
		opts.SHA = gh.String(existingSHA)
		if _, _, err := p.client.Repositories.UpdateFile(ctx, p.owner, p.repo, doc.Path, opts); err != nil {
			return "", fmt.Errorf("github: update file %s on %s: %w", doc.Path, doc.Branch, err)
		}
	}

	// Step 5: reuse an open pull request if there is one.
	prs, _, err := p.client.PullRequests.List(ctx, p.owner, p.repo, &gh.PullRequestListOptions{
		State: "open",
		Head:  p.owner + ":" + doc.Branch,
		Base:  p.base,
	})
	if err != nil {
		return "", fmt.Errorf("github: list pull requests for %s: %w", doc.Branch, err)
	}
	if len(prs) > 0 {
		number := prs[0].GetNumber()
		updated, _, err := p.client.PullRequests.Edit(ctx, p.owner, p.repo, number, &gh.PullRequest{
			Body: gh.String(doc.Body),
		})
		if err != nil {
			return "", fmt.Errorf("github: update pull request #%d for %s: %w", number, doc.Branch, err)
		}
		prURL := updated.GetHTMLURL()
		if prURL == "" {
			prURL = prs[0].GetHTMLURL()
		}
		p.logger.InfoContext(ctx, "github: updated rca pull request",
			slog.Int64("incident_id", doc.IncidentID),
			slog.String("pr_url", prURL),
		)
		return prURL, nil
	}

	// Step 6: open a new pull request.
	pr, _, err := p.client.PullRequests.Create(ctx, p.owner, p.repo, &gh.NewPullRequest{
		Title: gh.String(doc.Title),
		Head:  gh.String(doc.Branch),
		Base:  gh.String(p.base),
		Body:  gh.String(doc.Body),
	})
	if err != nil {
		return "", fmt.Errorf("github: create pull request for %s: %w", doc.Branch, err)
	}
	prURL := pr.GetHTMLURL()
	p.logger.InfoContext(ctx, "github: opened rca pull request",
		slog.Int64("incident_id", doc.IncidentID),
		slog.String("pr_url", prURL),
	)
	return prURL, nil
}

// isStatus reports whether err is a GitHub API error with the given HTTP status.
func isStatus(err error, status int) bool {
	var errResp *gh.ErrorResponse
	return errors.As(err, &errResp) && errResp.Response != nil && errResp.Response.StatusCode == status
}
