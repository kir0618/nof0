package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CycleRecord captures an end-to-end decision cycle for audit and analysis.
type CycleRecord struct {
	Timestamp     time.Time              `json:"timestamp"`
	TraderID      string                 `json:"trader_id"`
	ConfigVersion int64                  `json:"config_version,omitempty"`
	CycleNumber   int                    `json:"cycle_number"`
	PromptDigest  string                 `json:"prompt_digest,omitempty"`
	CoTTrace      string                 `json:"cot_trace,omitempty"`
	DecisionsJSON string                 `json:"decisions_json,omitempty"`
	Account       map[string]any         `json:"account_snapshot,omitempty"`
	Positions     []map[string]any       `json:"positions_snapshot,omitempty"`
	Candidates    []string               `json:"candidates,omitempty"`
	MarketDigest  map[string]any         `json:"market_snap_digest,omitempty"`
	Actions       []map[string]any       `json:"actions,omitempty"`
	Success       bool                   `json:"success"`
	ErrorMessage  string                 `json:"error_message,omitempty"`
	Extra         map[string]interface{} `json:"extra,omitempty"`
}

// Writer persists cycle records to a directory as JSON files (journal style).
type Writer struct {
	dir   string
	seq   int
	nowFn func() time.Time
}

// NewWriter constructs a journal writer.
func NewWriter(dir string) *Writer {
	if dir == "" {
		dir = "journal"
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Writer{dir: dir, nowFn: time.Now}
}

// WriteCycle writes a cycle record to a timestamped JSON file.
func (w *Writer) WriteCycle(rec *CycleRecord) (string, error) {
	if rec == nil {
		return "", fmt.Errorf("journal: nil record")
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = w.nowFn()
	}
	w.seq++
	rec.CycleNumber = w.seq
	name := fmt.Sprintf("cycle_%s_%05d.json", rec.Timestamp.UTC().Format("20060102_150405"), w.seq)
	path := filepath.Join(w.dir, name)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
