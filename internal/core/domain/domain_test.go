package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIncidentNumber(t *testing.T) {
	valid := []struct {
		name  string
		input string
		want  int64
	}{
		{"plain digits", "4821", 4821},
		{"surrounded by whitespace", " 4821 ", 4821},
		{"leading zeros", "000123", 123},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIncidentNumber(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	invalid := []string{"", "0", "0000", "abc", "12a", "-5", "12#", "1234567890"}
	for _, input := range invalid {
		t.Run("rejects "+input, func(t *testing.T) {
			_, err := ParseIncidentNumber(input)
			require.ErrorIs(t, err, ErrInvalidIncidentNumber)
		})
	}
}

func TestRCAFilePath_ConvertsToUTC(t *testing.T) {
	loc := time.FixedZone("PDT", -7*60*60)
	resolvedAt := time.Date(2026, 9, 27, 23, 30, 0, 0, loc)
	assert.Equal(t, "incidents/2026-09-28-4821.md", RCAFilePath(4821, resolvedAt))
}

func TestRCABranchName(t *testing.T) {
	assert.Equal(t, "incident-4821", RCABranchName(4821))
}

func TestRCAReport_Validate(t *testing.T) {
	tests := []struct {
		name    string
		report  RCAReport
		wantErr bool
	}{
		{"both set", RCAReport{Title: "Title", Summary: "Summary"}, false},
		{"blank title", RCAReport{Title: "", Summary: "Summary"}, true},
		{"whitespace-only title", RCAReport{Title: "   ", Summary: "Summary"}, true},
		{"blank summary", RCAReport{Title: "Title", Summary: ""}, true},
		{"whitespace-only summary", RCAReport{Title: "Title", Summary: "  \n "}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.report.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrInvalidRCA))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{"normal E.164 number", "+15555550123", "***0123"},
		{"exactly four characters", "1234", "***"},
		{"shorter than four characters", "12", "***"},
		{"empty", "", "***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MaskPhone(tt.phone))
		})
	}
}

func TestIncidentStatus_Valid(t *testing.T) {
	valid := []IncidentStatus{
		IncidentStatusOpen,
		IncidentStatusRCAPending,
		IncidentStatusRCAGenerating,
		IncidentStatusRCAComplete,
		IncidentStatusRCAFailed,
	}
	for _, s := range valid {
		t.Run(string(s), func(t *testing.T) {
			assert.True(t, s.Valid())
		})
	}

	invalid := []IncidentStatus{"", "closed", "OPEN", "unknown"}
	for _, s := range invalid {
		t.Run("invalid "+string(s), func(t *testing.T) {
			assert.False(t, s.Valid())
		})
	}
}
