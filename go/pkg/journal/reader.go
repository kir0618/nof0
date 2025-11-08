package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Reader loads journal cycle files from disk.
type Reader struct {
	dir string
}

// NewReader returns a reader rooted at dir (defaults to ./journal).
func NewReader(dir string) *Reader {
	if strings.TrimSpace(dir) == "" {
		dir = "journal"
	}
	return &Reader{dir: dir}
}

// List returns journal file paths ordered ascending by timestamp/name.
// If limit > 0, only the latest N files are returned.
func (r *Reader) List(limit int) ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, fmt.Errorf("journal: list dir %s: %w", r.dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasPrefix(name, "cycle_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		files = append(files, filepath.Join(r.dir, name))
	}
	sort.Strings(files)
	if limit > 0 && len(files) > limit {
		files = files[len(files)-limit:]
	}
	return files, nil
}

// Load reads a single cycle file.
func (r *Reader) Load(path string) (*CycleRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("journal: read %s: %w", path, err)
	}
	var rec CycleRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("journal: decode %s: %w", path, err)
	}
	return &rec, nil
}

// Latest loads the most recent N cycle records (ascending order).
func (r *Reader) Latest(limit int) ([]*CycleRecord, error) {
	files, err := r.List(limit)
	if err != nil {
		return nil, err
	}
	out := make([]*CycleRecord, 0, len(files))
	for _, path := range files {
		rec, err := r.Load(path)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}
