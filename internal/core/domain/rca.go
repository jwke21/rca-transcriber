package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RCAAuthor is the fixed Author field of every RCA document.
const RCAAuthor = "RCA Transcriber"

// RCAReport is the generated part of an RCA. Date and Author are not generated:
// Date comes from the incident's resolution time and Author is RCAAuthor.
type RCAReport struct {
	Title   string // short factual description, e.g. "Payments API errors caused by ledger deploy"
	Summary string // factual prose summary of the incident, from the transcript only
}

// Validate returns ErrInvalidRCA (wrapped with the reason) if Title or Summary is
// blank after trimming whitespace.
func (r RCAReport) Validate() error {
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("%w: title is blank", ErrInvalidRCA)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("%w: summary is blank", ErrInvalidRCA)
	}
	return nil
}

// RCADocument is everything the publisher needs to deliver one RCA.
type RCADocument struct {
	IncidentID int64
	Branch     string // RCABranchName(IncidentID)
	Path       string // RCAFilePath(IncidentID, resolution time)
	Title      string // PR title and commit subject
	Body       string // PR description
	Content    string // Markdown file contents
}

// RCABranchName returns "incident-{id}", e.g. "incident-4821".
func RCABranchName(incidentID int64) string {
	return "incident-" + strconv.FormatInt(incidentID, 10)
}

// RCAFilePath returns "incidents/{YYYY-MM-DD}-{id}.md" using the UTC date of resolvedAt,
// e.g. "incidents/2026-09-27-4821.md".
func RCAFilePath(incidentID int64, resolvedAt time.Time) string {
	date := resolvedAt.UTC().Format("2006-01-02")
	return fmt.Sprintf("incidents/%s-%d.md", date, incidentID)
}
