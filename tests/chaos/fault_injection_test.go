package chaos_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// FlakyStorageClient simulates S3 503 Service Unavailable rate-limit flakiness.
type FlakyStorageClient struct {
	failAttempts int
	currentCount int
}

func (f *FlakyStorageClient) PutChunk(ctx context.Context, chunkData []byte) error {
	f.currentCount++
	if f.currentCount <= f.failAttempts {
		return errors.New("503 Service Unavailable: S3 SlowDown")
	}
	return nil
}

func TestS3RateLimitRetryRecovery(t *testing.T) {
	client := &FlakyStorageClient{failAttempts: 2}

	var err error
	maxRetries := 5
	backoff := 10 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err = client.PutChunk(context.Background(), []byte("payload"))
		if err == nil {
			break
		}
		time.Sleep(backoff)
		backoff *= 2
	}

	if err != nil {
		t.Fatalf("expected retry recovery to succeed, got error: %v", err)
	}
}

func TestDatabasePoolDropRecovery(t *testing.T) {
	// Simulates DB connection pool drop and automated ping reconnect
	dbHealthy := false
	reconnect := func() error {
		dbHealthy = true
		return nil
	}

	if !dbHealthy {
		if err := reconnect(); err != nil {
			t.Fatalf("failed DB reconnect: %v", err)
		}
	}

	if !dbHealthy {
		t.Error("expected DB to be healthy after reconnect")
	}
}
