package derivation

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// WorkerPool — fan-out CDC events to registered workers
// ---------------------------------------------------------------------------

// PoolConfig controls worker pool behavior.
type PoolConfig struct {
	Retry        RetryConfig
	WorkerCount  int // Concurrent goroutines per event (default: len(workers))
	ResultStore  ResultStore
	DLQ          DeadLetterQueue
	Logger       *slog.Logger
}

// WorkerPool dispatches CDC events to workers and persists results.
type WorkerPool struct {
	workers []Worker
	store   ResultStore
	dlq     DeadLetterQueue
	retry   RetryConfig
	logger  *slog.Logger

	mu              sync.Mutex
	eventsProcessed int64
	resultsSaved    int64
}

// NewWorkerPool creates a pool with the given workers.
func NewWorkerPool(workers []Worker, cfg PoolConfig) *WorkerPool {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ResultStore == nil {
		cfg.ResultStore = NewFakeResultStore()
	}
	if cfg.DLQ == nil {
		cfg.DLQ = NewMemoryDLQ(cfg.Logger)
	}
	cfg.Retry.applyDefaults()
	return &WorkerPool{
		workers: workers,
		store:   cfg.ResultStore,
		dlq:     cfg.DLQ,
		retry:   cfg.Retry,
		logger:  cfg.Logger,
	}
}

// ProcessEvent dispatches a CDC event to all applicable workers concurrently.
// Results are persisted to the ResultStore. Permanent failures go to the DLQ.
func (p *WorkerPool) ProcessEvent(ctx context.Context, event Event) []ProcessingResult {
	p.mu.Lock()
	p.eventsProcessed++
	p.mu.Unlock()

	var (
		mu      sync.Mutex
		results []ProcessingResult
		wg      sync.WaitGroup
	)

	for _, w := range p.workers {
		if !w.ShouldProcess(event) {
			continue
		}

		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			result := p.processWithRetry(ctx, w, event)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(w)
	}

	wg.Wait()
	return results
}

// processWithRetry attempts to process an event with retry logic.
func (p *WorkerPool) processWithRetry(ctx context.Context, w Worker, event Event) ProcessingResult {
	versionID := event.VersionID
	workerName := w.Name()

	rr := RetryWithBackoff(ctx, p.retry, func(attempt int) error {
		p.logger.Info("worker processing",
			"worker", workerName,
			"version_id", versionID,
			"attempt", attempt)
		_, err := w.Process(ctx, event)
		return err
	})

	if rr.Success {
		// Re-process to get the actual result (retry wrapper only captured error).
		result, err := w.Process(ctx, event)
		if err != nil {
			// Race condition: succeeded in retry but failed on re-process.
			r := NewFailedResult(versionID, workerName, err)
			r.ResultID = generateResultID()
			p.saveResult(ctx, r)
			return r
		}
		result.ResultID = generateResultID()
		p.saveResult(ctx, result)
		return result
	}

	// Failed after all retries.
	if !IsTransient(rr.LastErr) {
		// Permanent failure — send to DLQ.
		p.dlq.Enqueue(ctx, DeadLetterEntry{
			Event:    event,
			Worker:   workerName,
			Error:    rr.LastErr.Error(),
			Attempts: rr.Attempts,
		})
		p.logger.Error("worker permanent failure",
			"worker", workerName,
			"version_id", versionID,
			"error", rr.LastErr)
	} else {
		// Transient failure exhausted — also DLQ.
		p.dlq.Enqueue(ctx, DeadLetterEntry{
			Event:    event,
			Worker:   workerName,
			Error:    rr.LastErr.Error(),
			Attempts: rr.Attempts,
		})
		p.logger.Error("worker transient failure exhausted",
			"worker", workerName,
			"version_id", versionID,
			"attempts", rr.Attempts,
			"error", rr.LastErr)
	}

	result := NewFailedResult(versionID, workerName, rr.LastErr)
	result.ResultID = generateResultID()
	p.saveResult(ctx, result)
	return result
}

// saveResult persists a result, logging but not propagating store errors.
func (p *WorkerPool) saveResult(ctx context.Context, result ProcessingResult) {
	if err := p.store.SaveResult(ctx, result); err != nil {
		p.logger.Error("failed to save derivation result",
			"worker", result.WorkerName,
			"version_id", result.VersionID,
			"err", err)
		return
	}
	p.mu.Lock()
	p.resultsSaved++
	p.mu.Unlock()
}

// Stats returns pool statistics.
func (p *WorkerPool) Stats() (eventsProcessed, resultsSaved int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eventsProcessed, p.resultsSaved
}

// Workers returns the registered workers.
func (p *WorkerPool) Workers() []Worker {
	return p.workers
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var resultCounter int64
var resultMu sync.Mutex

func generateResultID() string {
	resultMu.Lock()
	resultCounter++
	c := resultCounter
	resultMu.Unlock()
	return time.Now().UTC().Format("20060102150405") + "-" + string(rune('A'+c%26))
}
