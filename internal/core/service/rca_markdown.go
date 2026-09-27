package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

// rcaDateLayout formats the RCA's Date field as YYYY-MM-DD.
const rcaDateLayout = "2006-01-02"

// RenderRCAMarkdown renders the four-field RCA file (Title, Date, Author,
// Summary). It is pure and deterministic: the Date is the UTC date of
// resolvedAt, the Author is always domain.RCAAuthor, and the output ends with
// exactly one trailing newline.
func RenderRCAMarkdown(incidentID int64, report domain.RCAReport, resolvedAt time.Time) string {
	title := rcaNormalizeTitle(report.Title)
	summary := rcaNormalizeSummary(report.Summary)

	var b strings.Builder
	if title == "" {
		fmt.Fprintf(&b, "# Incident %d\n\n", incidentID)
	} else {
		fmt.Fprintf(&b, "# Incident %d: %s\n\n", incidentID, title)
	}
	fmt.Fprintf(&b, "**Date:** %s\n\n", resolvedAt.UTC().Format(rcaDateLayout))
	fmt.Fprintf(&b, "**Author:** %s\n\n", domain.RCAAuthor)
	b.WriteString("## Summary\n")
	if summary != "" {
		b.WriteString("\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}
	return b.String()
}

// BuildRCADocument assembles everything the publisher needs for one RCA. The PR
// title is always "docs: incident {id}"; the generated title only appears in
// the Markdown heading.
func BuildRCADocument(incident domain.Incident, report domain.RCAReport, resolvedAt time.Time) domain.RCADocument {
	return domain.RCADocument{
		IncidentID: incident.ID,
		Branch:     domain.RCABranchName(incident.ID),
		Path:       domain.RCAFilePath(incident.ID, resolvedAt),
		Title:      rcaPRTitle(incident.ID),
		Body:       rcaPRBody(incident.ID),
		Content:    RenderRCAMarkdown(incident.ID, report, resolvedAt),
	}
}

// rcaPRTitle returns the pull request title, e.g. "docs: incident 4821".
func rcaPRTitle(incidentID int64) string {
	return fmt.Sprintf("docs: incident %d", incidentID)
}

// rcaPRBody returns the pull request description.
func rcaPRBody(incidentID int64) string {
	return fmt.Sprintf("Automatically generated RCA draft for incident %d, from the on-call engineer's voice narration.\n"+
		"\n"+
		"Before merging:\n"+
		"- [ ] The title and summary match what happened\n"+
		"- [ ] Nothing in the summary is invented", incidentID)
}

// rcaNormalizeTitle trims the title and replaces every line break (CRLF, LF or
// CR) with a single space, so the Markdown heading stays on one line.
func rcaNormalizeTitle(title string) string {
	newlines := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
	return newlines.Replace(strings.TrimSpace(title))
}

// rcaNormalizeSummary converts CRLF line endings to LF and trims surrounding
// whitespace, keeping paragraph breaks intact.
func rcaNormalizeSummary(summary string) string {
	return strings.TrimSpace(strings.ReplaceAll(summary, "\r\n", "\n"))
}
