package derivation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers — fake workers
// ---------------------------------------------------------------------------

type fakeWorker struct {
	name          string
	mimeTypes     []string // mime types this worker handles
	processFn     func(ctx context.Context, event Event) (ProcessingResult, error)
	processCount  atomic.Int32
	failCount     atomic.Int32
	failUntil     int // fail for the first N calls, then succeed
}

func newFakeWorker(name string, mimeTypes ...string) *fakeWorker {
	return &fakeWorker{name: name, mimeTypes: mimeTypes}
}

func (w *fakeWorker) Name() string { return w.name }

func (w *fakeWorker) ShouldProcess(event Event) bool {
	if len(w.mimeTypes) == 0 {
		return true // handles everything
	}
	for _, mt := range w.mimeTypes {
		if strings.HasPrefix(event.MimeType, mt) {
			return true
		}
	}
	return false
}

func (w *fakeWorker) Process(ctx context.Context, event Event) (ProcessingResult, error) {
	w.processCount.Add(1)
	if w.processFn != nil {
		return w.processFn(ctx, event)
	}
	n := int(w.failCount.Add(1))
	if w.failUntil > 0 && n <= w.failUntil {
		return ProcessingResult{}, fmt.Errorf("transient failure: connection reset")
	}
	r := NewSuccessResult(event.VersionID, w.name, map[string]interface{}{
		"worker": w.name,
		"status": "ok",
	})
	return r, nil
}

// ---------------------------------------------------------------------------
// Test event helper
// ---------------------------------------------------------------------------

func testEvent(versionID, mimeType string) Event {
	return Event{
		EventID:       "evt-001",
		EventType:     "VERSION_COMMITTED",
		VersionID:     versionID,
		NodeID:        "node-001",
		TenantID:      "tenant-001",
		TotalSize:     1024 * 1024,
		MimeType:      mimeType,
		ContentSHA256: strings.Repeat("ab", 32),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Chunks: []ChunkDetail{
			{Index: 0, BlockHash: strings.Repeat("cd", 32), SizeBytes: 1024 * 1024, OffsetBytes: 0},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests — WorkerPool
// ---------------------------------------------------------------------------

func TestProcessEvent_BasicDispatch(t *testing.T) {
	store := NewFakeResultStore()
	worker := newFakeWorker("scanner")
	pool := NewWorkerPool([]Worker{worker}, PoolConfig{ResultStore: store})

	event := testEvent("v1", "application/pdf")
	results := pool.ProcessEvent(context.Background(), event)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusSuccess {
		t.Errorf("status = %q, want SUCCESS", results[0].Status)
	}
	if results[0].WorkerName != "scanner" {
		t.Errorf("worker = %q, want scanner", results[0].WorkerName)
	}
	if store.Count() != 1 {
		t.Errorf("expected 1 stored result, got %d", store.Count())
	}
}

func TestProcessEvent_ShouldProcessFiltering(t *testing.T) {
	store := NewFakeResultStore()
	clamav := newFakeWorker("clamav", "application/", "image/")
	ocr := newFakeWorker("ocr", "image/", "application/pdf")
	ffmpeg := newFakeWorker("ffmpeg", "video/")

	pool := NewWorkerPool([]Worker{clamav, ocr, ffmpeg}, PoolConfig{ResultStore: store})

	// Image → clamav + ocr should process, ffmpeg should not
	event := testEvent("v1", "image/png")
	results := pool.ProcessEvent(context.Background(), event)

	workersCalled := map[string]bool{}
	for _, r := range results {
		workersCalled[r.WorkerName] = true
	}

	if !workersCalled["clamav"] {
		t.Error("clamav should process image/png")
	}
	if !workersCalled["ocr"] {
		t.Error("ocr should process image/png")
	}
	if workersCalled["ffmpeg"] {
		t.Error("ffmpeg should NOT process image/png")
	}

	// Video → only ffmpeg
	event2 := testEvent("v2", "video/mp4")
	results2 := pool.ProcessEvent(context.Background(), event2)

	workersCalled2 := map[string]bool{}
	for _, r := range results2 {
		workersCalled2[r.WorkerName] = true
	}
	if workersCalled2["clamav"] {
		t.Error("clamav should NOT process video/mp4")
	}
	if workersCalled2["ocr"] {
		t.Error("ocr should NOT process video/mp4")
	}
	if !workersCalled2["ffmpeg"] {
		t.Error("ffmpeg should process video/mp4")
	}
}

func TestProcessEvent_RetryOnTransientFailure(t *testing.T) {
	store := NewFakeResultStore()
	worker := newFakeWorker("flaky")
	worker.failUntil = 2 // fail first 2 attempts, succeed on 3rd
	worker.processFn = func(ctx context.Context, event Event) (ProcessingResult, error) {
		n := int(worker.failCount.Add(1))
		if n <= 2 {
			return ProcessingResult{}, fmt.Errorf("connection reset by peer")
		}
		r := NewSuccessResult(event.VersionID, "flaky", map[string]interface{}{"ok": true})
		return r, nil
	}

	pool := NewWorkerPool([]Worker{worker}, PoolConfig{
		ResultStore: store,
		Retry: RetryConfig{
			MaxAttempts: 5,
			BaseDelay:   1 * time.Millisecond,
			MaxDelay:    5 * time.Millisecond,
		},
	})

	results := pool.ProcessEvent(context.Background(), testEvent("v1", "text/plain"))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusSuccess {
		t.Errorf("status = %q, want SUCCESS (should have retried)", results[0].Status)
	}
}

func TestProcessEvent_PermanentFailureSendsToDLQ(t *testing.T) {
	store := NewFakeResultStore()
	dlq := NewMemoryDLQ(slog.Default())
	worker := newFakeWorker("broken")
	worker.processFn = func(ctx context.Context, event Event) (ProcessingResult, error) {
		return ProcessingResult{}, fmt.Errorf("malformed input: unsupported format")
	}

	pool := NewWorkerPool([]Worker{worker}, PoolConfig{
		ResultStore: store,
		DLQ:         dlq,
		Retry: RetryConfig{
			MaxAttempts: 3,
			BaseDelay:   1 * time.Millisecond,
		},
	})

	results := pool.ProcessEvent(context.Background(), testEvent("v1", "text/plain"))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusFailed {
		t.Errorf("status = %q, want FAILED", results[0].Status)
	}
	if dlq.Size() != 1 {
		t.Errorf("DLQ size = %d, want 1", dlq.Size())
	}
}

func TestProcessEvent_TransientExhaustionSendsToDLQ(t *testing.T) {
	store := NewFakeResultStore()
	dlq := NewMemoryDLQ(slog.Default())
	worker := newFakeWorker("network-flaky")
	worker.processFn = func(ctx context.Context, event Event) (ProcessingResult, error) {
		return ProcessingResult{}, fmt.Errorf("i/o timeout")
	}

	pool := NewWorkerPool([]Worker{worker}, PoolConfig{
		ResultStore: store,
		DLQ:         dlq,
		Retry: RetryConfig{
			MaxAttempts: 2,
			BaseDelay:   1 * time.Millisecond,
		},
	})

	results := pool.ProcessEvent(context.Background(), testEvent("v1", "text/plain"))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusFailed {
		t.Errorf("status = %q, want FAILED", results[0].Status)
	}
	if dlq.Size() != 1 {
		t.Errorf("DLQ size = %d, want 1", dlq.Size())
	}
}

func TestProcessEvent_ConcurrentWorkers(t *testing.T) {
	store := NewFakeResultStore()
	var workers []Worker
	for i := 0; i < 5; i++ {
		workers = append(workers, newFakeWorker(fmt.Sprintf("w%d", i)))
	}
	pool := NewWorkerPool(workers, PoolConfig{ResultStore: store})

	event := testEvent("v1", "application/octet-stream")
	results := pool.ProcessEvent(context.Background(), event)

	if len(results) != 5 {
		t.Fatalf("expected 5 results (all workers handle octet-stream), got %d", len(results))
	}
	if store.Count() != 5 {
		t.Errorf("expected 5 stored results, got %d", store.Count())
	}

	// All workers should have been called.
	for _, r := range results {
		if r.Status != StatusSuccess {
			t.Errorf("worker %s: status = %q, want SUCCESS", r.WorkerName, r.Status)
		}
	}
}

func TestProcessEvent_NoWorkers(t *testing.T) {
	pool := NewWorkerPool([]Worker{}, PoolConfig{})
	results := pool.ProcessEvent(context.Background(), testEvent("v1", "text/plain"))
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestStats(t *testing.T) {
	pool := NewWorkerPool([]Worker{newFakeWorker("w1")}, PoolConfig{})
	pool.ProcessEvent(context.Background(), testEvent("v1", "text/plain"))
	pool.ProcessEvent(context.Background(), testEvent("v2", "text/plain"))

	processed, saved := pool.Stats()
	if processed != 2 {
		t.Errorf("eventsProcessed = %d, want 2", processed)
	}
	if saved != 2 {
		t.Errorf("resultsSaved = %d, want 2", saved)
	}
}

// ---------------------------------------------------------------------------
// Tests — Retry logic
// ---------------------------------------------------------------------------

func TestRetryWithBackoff_Success(t *testing.T) {
	var attempts atomic.Int32
	result := RetryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
	}, func(attempt int) error {
		attempts.Add(1)
		return nil
	})

	if !result.Success {
		t.Error("expected success")
	}
	if result.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", result.Attempts)
	}
}

func TestRetryWithBackoff_TransientThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	result := RetryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   1 * time.Millisecond,
	}, func(attempt int) error {
		n := int(attempts.Add(1))
		if n < 3 {
			return fmt.Errorf("connection reset")
		}
		return nil
	})

	if !result.Success {
		t.Error("expected success after retries")
	}
	if result.Attempts < 3 {
		t.Errorf("attempts = %d, expected >= 3", result.Attempts)
	}
}

func TestRetryWithBackoff_PermanentFailure(t *testing.T) {
	result := RetryWithBackoff(context.Background(), RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   1 * time.Millisecond,
	}, func(attempt int) error {
		return fmt.Errorf("malformed data: unsupported format")
	})

	if result.Success {
		t.Error("expected failure for permanent error")
	}
	if result.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry for permanent)", result.Attempts)
	}
}

func TestRetryWithBackoff_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	result := RetryWithBackoff(ctx, RetryConfig{
		MaxAttempts: 100,
		BaseDelay:   10 * time.Millisecond,
	}, func(attempt int) error {
		return fmt.Errorf("connection refused")
	})

	if result.Success {
		t.Error("expected failure after context cancel")
	}
}

// ---------------------------------------------------------------------------
// Tests — IsTransient
// ---------------------------------------------------------------------------

func TestIsTransient(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("connection reset by peer"), true},
		{fmt.Errorf("i/o timeout"), true},
		{fmt.Errorf("EOF"), true},
		{fmt.Errorf("context deadline exceeded"), true},
		{fmt.Errorf("malformed input"), false},
		{fmt.Errorf("unsupported format version 3"), false},
		{nil, false},
	}

	for _, tt := range tests {
		got := IsTransient(tt.err)
		if got != tt.want {
			t.Errorf("IsTransient(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests — DLQ
// ---------------------------------------------------------------------------

func TestMemoryDLQ_EnqueueDrain(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()

	dlq.Enqueue(ctx, DeadLetterEntry{
		Event:  testEvent("v1", "text/plain"),
		Worker: "w1",
		Error:  "failed",
	})

	if dlq.Size() != 1 {
		t.Fatalf("size = %d, want 1", dlq.Size())
	}

	entries, err := dlq.Drain(ctx, 10)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("drained = %d, want 1", len(entries))
	}
	if dlq.Size() != 0 {
		t.Errorf("size after drain = %d, want 0", dlq.Size())
	}
}

func TestMemoryDLQ_DrainPartial(t *testing.T) {
	dlq := NewMemoryDLQ(nil)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		dlq.Enqueue(ctx, DeadLetterEntry{
			Event:  testEvent(fmt.Sprintf("v%d", i), "text/plain"),
			Worker: "w1",
			Error:  "failed",
		})
	}

	entries, _ := dlq.Drain(ctx, 3)
	if len(entries) != 3 {
		t.Fatalf("drained = %d, want 3", len(entries))
	}
	if dlq.Size() != 2 {
		t.Errorf("size after drain = %d, want 2", dlq.Size())
	}
}

// ---------------------------------------------------------------------------
// Tests — ResultStore
// ---------------------------------------------------------------------------

func TestFakeResultStore_CRUD(t *testing.T) {
	store := NewFakeResultStore()
	ctx := context.Background()

	r1 := NewSuccessResult("v1", "clamav", map[string]interface{}{"clean": true})
	r1.ResultID = "r1"
	store.SaveResult(ctx, r1)

	r2 := NewFailedResult("v1", "ocr", fmt.Errorf("not an image"))
	r2.ResultID = "r2"
	store.SaveResult(ctx, r2)

	r3 := NewSuccessResult("v2", "clamav", nil)
	r3.ResultID = "r3"
	store.SaveResult(ctx, r3)

	results, err := store.GetResultsByVersion(ctx, "v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for v1, got %d", len(results))
	}

	all := store.Results()
	if len(all) != 3 {
		t.Errorf("total results = %d, want 3", len(all))
	}
}

func TestFakeResultStore_SaveError(t *testing.T) {
	store := NewFakeResultStore()
	store.SetSaveError(fmt.Errorf("db down"))

	err := store.SaveResult(context.Background(), NewSuccessResult("v1", "w1", nil))
	if err == nil {
		t.Error("expected error")
	}
}

// ---------------------------------------------------------------------------
// Tests — ChunkDetail and Event types
// ---------------------------------------------------------------------------

func TestChunkDetailJSON(t *testing.T) {
	event := testEvent("v1", "video/mp4")
	if len(event.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(event.Chunks))
	}
	c := event.Chunks[0]
	if c.Index != 0 {
		t.Errorf("index = %d, want 0", c.Index)
	}
	if c.SizeBytes != 1024*1024 {
		t.Errorf("size = %d, want 1048576", c.SizeBytes)
	}
}

func TestProcessingResultTypes(t *testing.T) {
	s := NewSuccessResult("v1", "w1", nil)
	if s.Status != StatusSuccess {
		t.Errorf("success status = %q", s.Status)
	}

	f := NewFailedResult("v1", "w1", fmt.Errorf("boom"))
	if f.Status != StatusFailed {
		t.Errorf("failed status = %q", f.Status)
	}
	if f.Error != "boom" {
		t.Errorf("error = %q, want boom", f.Error)
	}

	sk := NewSkippedResult("v1", "w1")
	if sk.Status != StatusSkipped {
		t.Errorf("skipped status = %q", sk.Status)
	}
}
