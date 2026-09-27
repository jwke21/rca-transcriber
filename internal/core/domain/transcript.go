package domain

import "time"

// TranscriptSegment is one result from the speech-to-text provider.
type TranscriptSegment struct {
	Text     string
	SpokenAt time.Time // wall-clock start of the utterance, UTC
	Duration time.Duration
	IsFinal  bool
}
