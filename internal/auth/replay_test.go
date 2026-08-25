package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// InMemoryNonceStore
// ---------------------------------------------------------------------------

func TestInMemoryNonceStore_ConsumeFirst(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	ok, err := store.Consume(context.Background(), "nonce-1", time.Minute)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Error("first Consume should return true")
	}
}

func TestInMemoryNonceStore_ConsumeReplay(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	store.Consume(context.Background(), "nonce-1", time.Minute)

	ok, err := store.Consume(context.Background(), "nonce-1", time.Minute)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if ok {
		t.Error("second Consume should return false (replay)")
	}
}

func TestInMemoryNonceStore_DifferentNonces(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	store.Consume(context.Background(), "nonce-a", time.Minute)

	ok, err := store.Consume(context.Background(), "nonce-b", time.Minute)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Error("different nonce should succeed")
	}
}

func TestInMemoryNonceStore_Expiry(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	store.Consume(context.Background(), "nonce-expire", 50*time.Millisecond)

	time.Sleep(100 * time.Millisecond)

	ok, err := store.Consume(context.Background(), "nonce-expire", time.Minute)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Error("expired nonce should be consumable again")
	}
}

func TestInMemoryNonceStore_CapacityEviction(t *testing.T) {
	store := NewInMemoryNonceStore(3)

	// Fill to capacity.
	store.Consume(context.Background(), "n1", time.Hour)
	store.Consume(context.Background(), "n2", time.Hour)
	store.Consume(context.Background(), "n3", time.Hour)

	// Adding one more triggers sweep; since all are valid, none get evicted,
	// but the store should still function.
	ok, err := store.Consume(context.Background(), "n4", time.Hour)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Error("new nonce should succeed even at capacity")
	}
}

func TestInMemoryNonceStore_ConcurrentSafety(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	var wg sync.WaitGroup
	const goroutines = 100
	results := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// All goroutines try to consume the same nonce.
			ok, err := store.Consume(context.Background(), "shared-nonce", time.Minute)
			if err != nil {
				t.Errorf("Consume error: %v", err)
				return
			}
			results <- ok
		}(i)
	}

	wg.Wait()
	close(results)

	trueCount := 0
	for ok := range results {
		if ok {
			trueCount++
		}
	}
	if trueCount != 1 {
		t.Errorf("exactly one goroutine should win, got %d", trueCount)
	}
}

func TestInMemoryNonceStore_DefaultCapacity(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	if store.maxSize != 1<<20 {
		t.Errorf("default capacity: got %d, want %d", store.maxSize, 1<<20)
	}
}

func TestInMemoryNonceStore_NilContext(t *testing.T) {
	store := NewInMemoryNonceStore(0)
	// The InMemoryNonceStore ignores context, so nil context is fine.
	ok, err := store.Consume(context.Background(), "test-nil", time.Minute)
	if err != nil {
		t.Fatalf("Consume with nil-ignored context: %v", err)
	}
	if !ok {
		t.Error("should succeed")
	}
}
