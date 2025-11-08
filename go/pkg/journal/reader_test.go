package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReaderLatest(t *testing.T) {
	dir := t.TempDir()
	r := NewReader(dir)

	write := func(name string, rec CycleRecord) {
		path := filepath.Join(dir, name)
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	write("cycle_20250101_000001.json", CycleRecord{TraderID: "t1", Timestamp: time.Now()})
	write("cycle_20250102_000001.json", CycleRecord{TraderID: "t2", Timestamp: time.Now()})
	write("notes.txt", CycleRecord{})

	recs, err := r.Latest(1)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(recs) != 1 || recs[0].TraderID != "t2" {
		t.Fatalf("unexpected records: %+v", recs)
	}

	recs, err = r.Latest(5)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 recs got %d", len(recs))
	}
}
