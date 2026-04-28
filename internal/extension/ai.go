package extension

import (
	"context"
	"io"

	"github.com/google/uuid"
)

// AIProvider abstracts AI service integrations for the platform.
// Implementations can use local models (Whisper) or cloud services.
type AIProvider interface {
	// Transcribe converts audio to text.
	Transcribe(ctx context.Context, audio io.Reader, format string, language string) (*TranscriptionResult, error)
	// Summarize generates a summary of the given text.
	Summarize(ctx context.Context, text string, maxLength int) (string, error)
	// Translate translates text from one language to another.
	Translate(ctx context.Context, text, fromLang, toLang string) (string, error)
	// ExtractTasks identifies action items from meeting/chat content.
	ExtractTasks(ctx context.Context, text string) ([]ExtractedTask, error)
}

// TranscriptionResult contains the output of speech-to-text processing.
type TranscriptionResult struct {
	Text     string              `json:"text"`
	Language string              `json:"language"`
	Segments []TranscriptSegment `json:"segments,omitempty"`
}

// TranscriptSegment represents a timestamped portion of a transcription.
type TranscriptSegment struct {
	StartTime float64 `json:"start_time"` // seconds
	EndTime   float64 `json:"end_time"`
	Text      string  `json:"text"`
	Speaker   string  `json:"speaker,omitempty"`
}

// ExtractedTask represents an action item extracted from text.
type ExtractedTask struct {
	Description string     `json:"description"`
	AssigneeHint string    `json:"assignee_hint,omitempty"` // Name mentioned in text
	DueDate      string    `json:"due_date,omitempty"`      // Relative date from text
	Priority     string    `json:"priority,omitempty"`      // high, medium, low
}

// MeetingSummary contains an AI-generated meeting summary.
type MeetingSummary struct {
	CallID      uuid.UUID       `json:"call_id"`
	Summary     string          `json:"summary"`
	KeyPoints   []string        `json:"key_points"`
	ActionItems []ExtractedTask `json:"action_items"`
	Duration    int             `json:"duration"` // seconds
}
