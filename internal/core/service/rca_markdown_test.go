package service

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

// rcaTestUpdateGolden regenerates the golden files:
//
//	go test ./internal/core/service -run RCAMarkdown -update-rca-golden
var rcaTestUpdateGolden = flag.Bool("update-rca-golden", false, "rewrite testdata/rca_*.golden.md")

func rcaTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return ts
}

func TestRCAMarkdown_Golden(t *testing.T) {
	tests := []struct {
		name       string
		golden     string
		incidentID int64
		report     domain.RCAReport
		resolvedAt string
	}{
		{
			name:       "single paragraph",
			golden:     "rca_single_paragraph.golden.md",
			incidentID: 4821,
			report: domain.RCAReport{
				Title:   "  Payments API errors caused by ledger deploy \n",
				Summary: "\n  At 14:02 UTC the payments API started returning 500 errors after the ledger service deploy. The engineer rolled back the deploy at 14:20 and error rates returned to normal.  \n",
			},
			resolvedAt: "2026-09-27T14:25:00Z",
		},
		{
			name:       "multi paragraph with CRLF",
			golden:     "rca_multi_paragraph.golden.md",
			incidentID: 77,
			report: domain.RCAReport{
				Title: "Checkout latency from exhausted database connections",
				Summary: "\r\nCheckout p99 latency rose above 4 seconds at 09:10 UTC.\r\n\r\n" +
					"The engineer found the orders database connection pool at its limit of 50.\r\n" +
					"A batch export job had been holding connections open.\r\n\r\n" +
					"The export job was paused at 09:40 and latency recovered by 09:45.\r\n\r\n",
			},
			resolvedAt: "2026-09-27T23:30:00-07:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderRCAMarkdown(tt.incidentID, tt.report, rcaTestTime(t, tt.resolvedAt))
			path := filepath.Join("testdata", tt.golden)

			if *rcaTestUpdateGolden {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "missing golden file; run with -update-rca-golden")
			assert.Equal(t, string(want), got)
			assert.NotContains(t, got, "\r")
			assert.True(t, strings.HasSuffix(got, "\n") && !strings.HasSuffix(got, "\n\n"),
				"file must end with exactly one newline")
		})
	}
}

func TestRCAMarkdown_Title(t *testing.T) {
	resolvedAt := rcaTestTime(t, "2026-09-27T10:00:00Z")
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"plain", "Disk full on db-1", "# Incident 5: Disk full on db-1"},
		{"surrounding whitespace", "  \n Disk full on db-1 \n\t", "# Incident 5: Disk full on db-1"},
		{"LF", "Disk full\non db-1", "# Incident 5: Disk full on db-1"},
		{"CRLF", "Disk full\r\non db-1", "# Incident 5: Disk full on db-1"},
		{"CR", "Disk full\ron db-1", "# Incident 5: Disk full on db-1"},
		{"several lines", "Disk\nfull\r\non\rdb-1", "# Incident 5: Disk full on db-1"},
		{"blank", " \n ", "# Incident 5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderRCAMarkdown(5, domain.RCAReport{Title: tt.title, Summary: "Summary."}, resolvedAt)
			firstLine, rest, ok := strings.Cut(got, "\n")
			require.True(t, ok)
			assert.Equal(t, tt.want, firstLine)
			assert.True(t, strings.HasPrefix(rest, "\n**Date:** 2026-09-27\n"), "heading must be one line: %q", got)
		})
	}
}

func TestRCAMarkdown_Date(t *testing.T) {
	tests := []struct {
		name       string
		resolvedAt string
		want       string
	}{
		{"UTC", "2026-09-27T12:00:00Z", "**Date:** 2026-09-27"},
		{"negative offset rolls to next UTC day", "2026-09-27T23:30:00-07:00", "**Date:** 2026-09-28"},
		{"positive offset rolls to previous UTC day", "2026-09-28T01:30:00+09:00", "**Date:** 2026-09-27"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderRCAMarkdown(1, domain.RCAReport{Title: "T", Summary: "S"}, rcaTestTime(t, tt.resolvedAt))
			assert.Contains(t, strings.Split(got, "\n"), tt.want)
		})
	}
}

func TestRCAMarkdown_Author(t *testing.T) {
	tests := []struct {
		name   string
		report domain.RCAReport
	}{
		{"normal report", domain.RCAReport{Title: "T", Summary: "S"}},
		{"summary mentions another author", domain.RCAReport{Title: "Written by Alice", Summary: "Author: Bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderRCAMarkdown(9, tt.report, rcaTestTime(t, "2026-09-27T12:00:00Z"))
			lines := strings.Split(got, "\n")
			require.GreaterOrEqual(t, len(lines), 5)
			assert.Equal(t, "**Author:** RCA Transcriber", lines[4])
			assert.Equal(t, "**Author:** "+domain.RCAAuthor, lines[4])
		})
	}
}

func TestRCAMarkdown_Summary(t *testing.T) {
	resolvedAt := rcaTestTime(t, "2026-09-27T12:00:00Z")
	header := "# Incident 3: T\n\n**Date:** 2026-09-27\n\n**Author:** RCA Transcriber\n\n## Summary\n"
	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{"trimmed", "\n\n  One line.  \n\n", header + "\nOne line.\n"},
		{"paragraphs kept", "First.\n\nSecond.\nStill second.", header + "\nFirst.\n\nSecond.\nStill second.\n"},
		{"CRLF normalized", "First.\r\n\r\nSecond.\r\n", header + "\nFirst.\n\nSecond.\n"},
		{"blank summary still ends with one newline", " \r\n ", header},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderRCAMarkdown(3, domain.RCAReport{Title: "T", Summary: tt.summary}, resolvedAt)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRCAMarkdown_BuildRCADocument(t *testing.T) {
	report := domain.RCAReport{Title: "Model title: do not use in PR", Summary: "Something happened."}
	tests := []struct {
		name       string
		incident   domain.Incident
		resolvedAt string
		wantBranch string
		wantPath   string
		wantTitle  string
		wantBody   string
	}{
		{
			name:       "typical incident",
			incident:   domain.Incident{ID: 4821, Status: domain.IncidentStatusRCAGenerating},
			resolvedAt: "2026-09-27T14:25:00Z",
			wantBranch: "incident-4821",
			wantPath:   "incidents/2026-09-27-4821.md",
			wantTitle:  "docs: incident 4821",
			wantBody: "Automatically generated RCA draft for incident 4821, from the on-call engineer's voice narration.\n" +
				"\n" +
				"Before merging:\n" +
				"- [ ] The title and summary match what happened\n" +
				"- [ ] Nothing in the summary is invented",
		},
		{
			name:       "path uses UTC date",
			incident:   domain.Incident{ID: 7},
			resolvedAt: "2026-09-27T23:30:00-07:00",
			wantBranch: "incident-7",
			wantPath:   "incidents/2026-09-28-7.md",
			wantTitle:  "docs: incident 7",
			wantBody: "Automatically generated RCA draft for incident 7, from the on-call engineer's voice narration.\n" +
				"\n" +
				"Before merging:\n" +
				"- [ ] The title and summary match what happened\n" +
				"- [ ] Nothing in the summary is invented",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolvedAt := rcaTestTime(t, tt.resolvedAt)
			doc := BuildRCADocument(tt.incident, report, resolvedAt)

			assert.Equal(t, tt.incident.ID, doc.IncidentID)
			assert.Equal(t, tt.wantBranch, doc.Branch)
			assert.Equal(t, tt.wantPath, doc.Path)
			assert.Equal(t, tt.wantTitle, doc.Title)
			assert.NotContains(t, doc.Title, report.Title)
			assert.Equal(t, tt.wantBody, doc.Body)
			assert.Equal(t, RenderRCAMarkdown(tt.incident.ID, report, resolvedAt), doc.Content)
		})
	}
}
