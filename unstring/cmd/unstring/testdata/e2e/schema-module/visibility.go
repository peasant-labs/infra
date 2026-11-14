package schema

// TranscriptVisibility is who may read a transcript.
type TranscriptVisibility string

const (
	VisibilityPublic  TranscriptVisibility = "public"
	VisibilityPrivate TranscriptVisibility = "private"
)
