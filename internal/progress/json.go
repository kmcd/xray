package progress

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// JSONSink emits one NDJSON object per Event to a writer. The wire
// shape matches docs/spec.md "JSON event schema" — additive changes are
// non-breaking; renames or removals bump output_schema_version.
type JSONSink struct {
	w  io.Writer
	mu sync.Mutex
}

// NewJSONSink wraps a writer (typically stdout). Concurrent Emit calls
// are serialised so NDJSON lines never interleave.
func NewJSONSink(w io.Writer) *JSONSink {
	return &JSONSink{w: w}
}

// wireEvent is the JSON projection. Fields are omitted when empty so the
// stream stays compact and humans can scan it.
type wireEvent struct {
	TS        string         `json:"ts"`
	Kind      EventKind      `json:"kind"`
	Repo      string         `json:"repo,omitempty"`
	Connector string         `json:"connector,omitempty"`
	Phase     string         `json:"phase,omitempty"`
	Done      int64          `json:"done,omitempty"`
	Total     int64          `json:"total,omitempty"`
	Message   string         `json:"msg,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// Emit writes one NDJSON line per event. The mutex keeps lines atomic
// across concurrent goroutines.
func (s *JSONSink) Emit(e Event) {
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	wire := wireEvent{
		TS:        at.UTC().Format(time.RFC3339),
		Kind:      e.Kind,
		Repo:      e.Repo,
		Connector: e.Connector,
		Phase:     e.Phase,
		Done:      e.Done,
		Total:     e.Total,
		Message:   e.Message,
		Fields:    e.Fields,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(wire)
}
