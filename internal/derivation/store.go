package derivation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// ResultStore — persists derivation results
// ---------------------------------------------------------------------------

// ResultStore abstracts persistence for derivation results.
type ResultStore interface {
	SaveResult(ctx context.Context, result ProcessingResult) error
	GetResultsByVersion(ctx context.Context, versionID string) ([]ProcessingResult, error)
}

// ---------------------------------------------------------------------------
// FakeResultStore — in-memory for testing
// ---------------------------------------------------------------------------

// FakeResultStore is an in-memory ResultStore for tests.
type FakeResultStore struct {
	mu      sync.Mutex
	results []ProcessingResult
	saveErr error
}

// NewFakeResultStore creates a new fake store.
func NewFakeResultStore() *FakeResultStore {
	return &FakeResultStore{}
}

// SetSaveError configures the store to return an error on SaveResult.
func (f *FakeResultStore) SetSaveError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveErr = err
}

// SaveResult stores a result in memory.
func (f *FakeResultStore) SaveResult(_ context.Context, result ProcessingResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.results = append(f.results, result)
	return nil
}

// GetResultsByVersion returns all results for a version.
func (f *FakeResultStore) GetResultsByVersion(_ context.Context, versionID string) ([]ProcessingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ProcessingResult
	for _, r := range f.results {
		if r.VersionID == versionID {
			out = append(out, r)
		}
	}
	return out, nil
}

// Results returns all stored results (test helper).
func (f *FakeResultStore) Results() []ProcessingResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ProcessingResult, len(f.results))
	copy(out, f.results)
	return out
}

// Count returns the number of stored results.
func (f *FakeResultStore) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.results)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// MarshalResultData serializes result data to JSON bytes.
func MarshalResultData(data map[string]interface{}) ([]byte, error) {
	if data == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(data)
}

// FormatJSONB returns a string suitable for PostgreSQL JSONB insertion.
func FormatJSONB(data map[string]interface{}) string {
	b, err := MarshalResultData(data)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ValidateResult checks that a ProcessingResult has required fields.
func ValidateResult(r ProcessingResult) error {
	if r.VersionID == "" {
		return fmt.Errorf("derivation: result missing version_id")
	}
	if r.WorkerName == "" {
		return fmt.Errorf("derivation: result missing worker_name")
	}
	if r.Status == "" {
		return fmt.Errorf("derivation: result missing status")
	}
	return nil
}
