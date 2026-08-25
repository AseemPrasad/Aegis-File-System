package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Stub types for coverage tests
// ---------------------------------------------------------------------------

type stubBlobStore struct {
	uploadURL    string
	uploadErr    error
	uploadCalled bool
	verifyErr    error
	verifyCalled bool
}

func (s *stubBlobStore) GenerateUploadURL(_ context.Context, _, _ string, _ int64) (string, error) {
	s.uploadCalled = true
	return s.uploadURL, s.uploadErr
}

func (s *stubBlobStore) VerifyBlock(_ context.Context, _, _ string) error {
	s.verifyCalled = true
	return s.verifyErr
}

type capturingBus struct {
	lastEvent FileCommittedEvent
	captured  bool
}

func (b *capturingBus) PublishFileCommitted(_ context.Context, event FileCommittedEvent) error {
	b.lastEvent = event
	b.captured = true
	return nil
}

func (b *capturingBus) PublishBlockTombstone(_ context.Context, _ string, _ BlockTombstoneEvent) error {
	return nil
}

func newTestServerWithBlob(t *testing.T, store Store, blob BlobStore, events EventBus) (*IngressServer, *http.ServeMux) {
	t.Helper()
	metrics := NewIngestMetrics(nil)
	cfg := Config{APIToken: "tok123", EndpointID: "test-edge"}
	srv := NewIngressServer(store, &stubTokenSigner{}, blob, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)
	return srv, mux
}

// ---------------------------------------------------------------------------
// 1. BlobStore branch in handleInitiate
// ---------------------------------------------------------------------------

func TestInitiate_BlobStoreBranch(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	hash := "aabb000000000000000000000000000000000000000000000000000000000001"
	blob := &stubBlobStore{uploadURL: "https://blob.example.com/upload"}

	_, mux := newTestServerWithBlob(t, store, blob, &NoopBus{})
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "photo.jpg",
		TotalSize: 1024,
		Chunks:    []ChunkInfo{{BlockHash: hash, SizeBytes: 1024}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !blob.uploadCalled {
		t.Fatal("expected GenerateUploadURL to be called on BlobStore")
	}

	var resp InitiateResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.UploadURLs) != 1 {
		t.Fatalf("expected 1 upload URL, got %d", len(resp.UploadURLs))
	}
	if resp.UploadURLs[0].URL != "https://blob.example.com/upload" {
		t.Fatalf("unexpected URL: %s", resp.UploadURLs[0].URL)
	}
}

// ---------------------------------------------------------------------------
// 2. handleInitiate with existing NodeID
// ---------------------------------------------------------------------------

func TestInitiate_WithExistingNodeID(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	existingNodeID, _ := store.CreateFileNode(context.Background(), tenantID, nil, "existing.txt")

	hash := "aabb000000000000000000000000000000000000000000000000000000000001"
	nodeIDStr := existingNodeID.String()

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "photo.jpg",
		NodeID:    &nodeIDStr,
		TotalSize: 1024,
		Chunks:    []ChunkInfo{{BlockHash: hash, SizeBytes: 1024}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp InitiateResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.NodeID != nodeIDStr {
		t.Fatalf("expected node_id %s, got %s", nodeIDStr, resp.NodeID)
	}
}

// ---------------------------------------------------------------------------
// 3. handleInitiate with ParentID
// ---------------------------------------------------------------------------

func TestInitiate_WithParentID(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	parentID := uuid.New().String()
	hash := "aabb000000000000000000000000000000000000000000000000000000000001"

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "child.txt",
		ParentID:  &parentID,
		TotalSize: 512,
		Chunks:    []ChunkInfo{{BlockHash: hash, SizeBytes: 512}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp InitiateResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.NodeID == "" {
		t.Fatal("expected a non-empty node_id from CreateFileNode with parent")
	}
}

// ---------------------------------------------------------------------------
// 4. BlobStore branch in handleCommit
// ---------------------------------------------------------------------------

func TestCommit_BlobStoreBranch(t *testing.T) {
	tenantID := uuid.New()
	nodeID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 1024, 1, "127.0.0.1", "test")

	blob := &stubBlobStore{}
	hash := "ccdd000000000000000000000000000000000000000000000000000000000001"
	sha256 := "eeff0000000000000000000000000000000000000000000000000000000000ff"

	_, mux := newTestServerWithBlob(t, store, blob, &NoopBus{})
	body := mustJSON(CommitRequest{
		SessionID:     sid.String(),
		ContentSHA256: sha256,
		Blocks:        []BlockMeta{{BlockHash: hash, ChunkIndex: 0, Offset: 0, SizeBytes: 1024, ETag: "etag123"}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if !blob.verifyCalled {
		t.Fatal("expected VerifyBlock to be called on BlobStore")
	}
}

// ---------------------------------------------------------------------------
// 5. handleCommit with MimeType set
// ---------------------------------------------------------------------------

func TestCommit_WithMimeType(t *testing.T) {
	tenantID := uuid.New()
	nodeID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 1024, 1, "127.0.0.1", "test")

	bus := &capturingBus{}
	hash := "ccdd000000000000000000000000000000000000000000000000000000000001"
	sha256 := "eeff0000000000000000000000000000000000000000000000000000000000ff"

	metrics := NewIngestMetrics(nil)
	cfg := Config{APIToken: "tok123", EndpointID: "test-edge"}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, bus, metrics, cfg, nil)
	mux2 := http.NewServeMux()
	srv.SetupRoutes(mux2)

	body := mustJSON(CommitRequest{
		SessionID:     sid.String(),
		ContentSHA256: sha256,
		MimeType:      "image/png",
		Blocks:        []BlockMeta{{BlockHash: hash, ChunkIndex: 0, Offset: 0, SizeBytes: 1024}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux2.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if !bus.captured {
		t.Fatal("expected CDC event to be published")
	}
	if bus.lastEvent.MimeType != "image/png" {
		t.Fatalf("expected mime_type 'image/png', got %q", bus.lastEvent.MimeType)
	}
}

// ---------------------------------------------------------------------------
// 6. Session Reaper — Start / Stop
// ---------------------------------------------------------------------------

func TestSessionReaper_StartStop(t *testing.T) {
	store := NewFakeStore()
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, &NoopBus{}, NewIngestMetrics(nil), Config{}, nil)

	srv.StartSessionReaper(10 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	srv.StopSessionReaper()
}

func TestSessionReaper_StopWithoutStart(t *testing.T) {
	store := NewFakeStore()
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, &NoopBus{}, NewIngestMetrics(nil), Config{}, nil)

	srv.StopSessionReaper()
}

// ---------------------------------------------------------------------------
// 7. LoggingMiddleware
// ---------------------------------------------------------------------------

func TestLoggingMiddleware_Passthrough(t *testing.T) {
	srv := NewIngressServer(NewFakeStore(), &stubTokenSigner{}, nil, &NoopBus{}, NewIngestMetrics(nil), Config{}, nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	handler := srv.LoggingMiddleware(next)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Fatalf("expected body 'ok', got %q", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 8. Panic recovery in wrap
// ---------------------------------------------------------------------------

func TestWrap_PanicRecovery(t *testing.T) {
	store := NewFakeStore()
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, &NoopBus{}, NewIngestMetrics(nil), Config{}, nil)

	panicFn := func(w http.ResponseWriter, r *http.Request) error {
		panic("test panic")
	}
	handler := srv.wrap(panicFn)

	req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("{}")))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 9. statusWriter.WriteHeader
// ---------------------------------------------------------------------------

func TestStatusWriter_WriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	sw.WriteHeader(http.StatusCreated)

	if sw.status != http.StatusCreated {
		t.Fatalf("expected sw.status=%d, got %d", http.StatusCreated, sw.status)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected rec.Code=%d, got %d", http.StatusCreated, rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 10. NoopBus.PublishBlockTombstone with Logger
// ---------------------------------------------------------------------------

func TestNoopBus_PublishBlockTombstone_WithLogger(t *testing.T) {
	bus := &NoopBus{Logger: slog.Default()}
	err := bus.PublishBlockTombstone(context.Background(), "test-topic", BlockTombstoneEvent{
		BlockHash: "aabb",
		TenantID:  "t1",
		SizeBytes: 100,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reason:    "test",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestNoopBus_PublishBlockTombstone_NilLogger(t *testing.T) {
	bus := &NoopBus{}
	err := bus.PublishBlockTombstone(context.Background(), "test-topic", BlockTombstoneEvent{
		BlockHash: "aabb",
		TenantID:  "t1",
		SizeBytes: 100,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reason:    "test",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 11. decodeJSON edge cases (tested through initiate handler)
// ---------------------------------------------------------------------------

func TestDecodeJSON_EmptyBody(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400 for empty body, got %d", w.Code)
	}
}

func TestDecodeJSON_MalformedJSON(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", strings.NewReader("{not valid json"))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400 for malformed JSON, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// 12. handleInitiate BlobStore error
// ---------------------------------------------------------------------------

func TestInitiate_BlobStoreError(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	hash := "aabb000000000000000000000000000000000000000000000000000000000001"
	blob := &stubBlobStore{uploadErr: fmt.Errorf("blob store error")}

	_, mux := newTestServerWithBlob(t, store, blob, &NoopBus{})
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "photo.jpg",
		TotalSize: 1024,
		Chunks:    []ChunkInfo{{BlockHash: hash, SizeBytes: 1024}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 500 {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}
}
