package twilio

import (
	"strconv"
	"strings"
)

// speakDigits spells n digit by digit so text-to-speech reads "4 8 2 1"
// instead of "four thousand eight hundred twenty-one".
func speakDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	parts := make([]string, 0, len(s))
	for _, r := range s {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, " ")
}
