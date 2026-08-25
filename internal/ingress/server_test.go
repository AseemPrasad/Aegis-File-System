package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubTokenSigner struct {
	urls map[string]string
	err  error
}

func (s *stubTokenSigner) GeneratePreSignedURL(_ context.Context, tenantID, blockHash, endpointID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.urls != nil {
		if u, ok := s.urls[blockHash]; ok {
			return u, nil
		}
	}
	return fmt.Sprintf("https://blob/%s/%s?ep=%s", tenantID, blockHash, endpointID), nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func newTestServer(t *testing.T, store Store, apiToken string) (*IngressServer, *http.ServeMux) {
	t.Helper()
	metrics := NewIngestMetrics(nil)
	events := &NoopBus{}
	cfg := Config{APIToken: apiToken, EndpointID: "test-edge"}
	srv := NewIngressServer(store, &stubTokenSigner{}, nil, events, metrics, cfg, nil)
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)
	return srv, mux
}

func TestHealthz(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestReadyz_Healthy(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestReadyz_Degraded(t *testing.T) {
	store := NewFakeStore()
	store.SetPingErr(fmt.Errorf("db down"))
	_, mux := newTestServer(t, store, "tok123")
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestAuth_MissingToken(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate",
		bytes.NewReader(mustJSON(InitiateRequest{})))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_WrongToken(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate",
		bytes.NewReader(mustJSON(InitiateRequest{})))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_EmptyTokenConfig(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "")
	body := mustJSON(InitiateRequest{
		TenantID:  uuid.New().String(),
		FileName:  "test.txt",
		TotalSize: 100,
		Chunks:    []ChunkInfo{{BlockHash: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", SizeBytes: 100}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer anything")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == 401 {
		t.Fatalf("expected not 401 when token config is empty")
	}
}

func TestInitiate_QuotaExceeded(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 1000, 900)

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "big.bin",
		TotalSize: 200,
		Chunks:    []ChunkInfo{{BlockHash: "aabb000000000000000000000000000000000000000000000000000000000000", SizeBytes: 200}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 402 {
		t.Fatalf("expected 402, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInitiate_Success_NewFile(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	hash1 := "aabb000000000000000000000000000000000000000000000000000000000001"
	hash2 := "aabb000000000000000000000000000000000000000000000000000000000002"
	store.SeedCASBlock(hash1)

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "photo.jpg",
		TotalSize: 2048,
		Chunks: []ChunkInfo{
			{BlockHash: hash1, SizeBytes: 1024},
			{BlockHash: hash2, SizeBytes: 1024},
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp InitiateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatal("session_id must not be empty")
	}
	if resp.NodeID == "" {
		t.Fatal("node_id must not be empty")
	}
	if len(resp.UploadURLs) != 1 {
		t.Fatalf("expected 1 upload URL (hash1 in CAS), got %d", len(resp.UploadURLs))
	}
	if resp.UploadURLs[0].BlockHash != hash2 {
		t.Fatalf("expected upload URL for hash2, got %s", resp.UploadURLs[0].BlockHash)
	}
}

func TestCommit_Success(t *testing.T) {
	tenantID := uuid.New()
	nodeID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 1024, 2, "127.0.0.1", "test")

	hash1 := "ccdd000000000000000000000000000000000000000000000000000000000001"
	hash2 := "ccdd000000000000000000000000000000000000000000000000000000000002"
	sha256 := "eeff0000000000000000000000000000000000000000000000000000000000ff"

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(CommitRequest{
		SessionID:     sid.String(),
		ContentSHA256: sha256,
		Blocks: []BlockMeta{
			{BlockHash: hash1, ChunkIndex: 0, Offset: 0, SizeBytes: 512},
			{BlockHash: hash2, ChunkIndex: 1, Offset: 512, SizeBytes: 512},
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp CommitResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VersionNumber != 1 {
		t.Fatalf("expected version 1, got %d", resp.VersionNumber)
	}
}

func TestCommit_DuplicateCommit(t *testing.T) {
	tenantID := uuid.New()
	nodeID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)
	sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 100, 1, "127.0.0.1", "test")

	hash := "aa11000000000000000000000000000000000000000000000000000000000001"
	sha := "bb22000000000000000000000000000000000000000000000000000000000022"

	_, mux := newTestServer(t, store, "tok123")

	commitBody := mustJSON(CommitRequest{
		SessionID:     sid.String(),
		ContentSHA256: sha,
		Blocks:        []BlockMeta{{BlockHash: hash, ChunkIndex: 0, Offset: 0, SizeBytes: 100}},
	})

	req1 := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(commitBody))
	req1.Header.Set("Authorization", "Bearer tok123")
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)
	if w1.Code != 201 {
		t.Fatalf("first commit: expected 201, got %d", w1.Code)
	}

	req2 := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(commitBody))
	req2.Header.Set("Authorization", "Bearer tok123")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != 409 {
		t.Fatalf("second commit: expected 409, got %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestCommit_SessionExpired(t *testing.T) {
	tenantID := uuid.New()
	nodeID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)
	sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 100, 1, "127.0.0.1", "test")

	store.mu.Lock()
	rec := store.sessions[sid]
	rec.ExpiresAt = time.Now().Add(-1 * time.Minute)
	store.sessions[sid] = rec
	store.mu.Unlock()

	hash := "aa11000000000000000000000000000000000000000000000000000000000001"
	sha := "bb22000000000000000000000000000000000000000000000000000000000022"

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(CommitRequest{
		SessionID:     sid.String(),
		ContentSHA256: sha,
		Blocks:        []BlockMeta{{BlockHash: hash, ChunkIndex: 0, Offset: 0, SizeBytes: 100}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 410 {
		t.Fatalf("expected 410, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCommit_SessionNotFound(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	body := mustJSON(CommitRequest{
		SessionID:     uuid.New().String(),
		ContentSHA256: "bb22000000000000000000000000000000000000000000000000000000000022",
		Blocks:        []BlockMeta{{BlockHash: "aa11000000000000000000000000000000000000000000000000000000000001", ChunkIndex: 0, Offset: 0, SizeBytes: 100}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInitiate_InvalidRequest(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")

	tests := []struct {
		name string
		body any
	}{
		{"empty body", nil},
		{"missing tenant", InitiateRequest{
			FileName: "f", TotalSize: 1,
			Chunks: []ChunkInfo{{BlockHash: "aabb000000000000000000000000000000000000000000000000000000000001", SizeBytes: 1}},
		}},
		{"missing filename", InitiateRequest{
			TenantID: uuid.New().String(), TotalSize: 1,
			Chunks: []ChunkInfo{{BlockHash: "aabb000000000000000000000000000000000000000000000000000000000001", SizeBytes: 1}},
		}},
		{"zero size", InitiateRequest{
			TenantID: uuid.New().String(), FileName: "f",
			Chunks: []ChunkInfo{{BlockHash: "aabb000000000000000000000000000000000000000000000000000000000001", SizeBytes: 1}},
		}},
		{"empty chunks", InitiateRequest{
			TenantID: uuid.New().String(), FileName: "f", TotalSize: 1,
		}},
		{"bad hash len", InitiateRequest{
			TenantID: uuid.New().String(), FileName: "f", TotalSize: 1,
			Chunks: []ChunkInfo{{BlockHash: "abc", SizeBytes: 1}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Reader
			if tc.body == nil {
				body = bytes.NewReader(nil)
			} else {
				body = bytes.NewReader(mustJSON(tc.body))
			}
			req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", body)
			req.Header.Set("Authorization", "Bearer tok123")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != 400 {
				t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCommit_BlocksCountMismatch(t *testing.T) {
	tenantID := uuid.New()
	nodeID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)
	sid, _, _ := store.CreateUploadSession(context.Background(), tenantID, nodeID, 200, 2, "127.0.0.1", "test")

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(CommitRequest{
		SessionID:     sid.String(),
		ContentSHA256: "bb22000000000000000000000000000000000000000000000000000000000022",
		Blocks:        []BlockMeta{{BlockHash: "aa11000000000000000000000000000000000000000000000000000000000001", ChunkIndex: 0, Offset: 0, SizeBytes: 100}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInitiate_UnknownTenant(t *testing.T) {
	_, mux := newTestServer(t, NewFakeStore(), "tok123")
	body := mustJSON(InitiateRequest{
		TenantID:  uuid.New().String(),
		FileName:  "test.txt",
		TotalSize: 100,
		Chunks:    []ChunkInfo{{BlockHash: "aabb000000000000000000000000000000000000000000000000000000000001", SizeBytes: 100}},
	})
	req := httptest.NewRequest("POST", "/api/v1/ingest/initiate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInitiate_AllBlocksInCAS(t *testing.T) {
	tenantID := uuid.New()
	store := NewFakeStore()
	store.SeedTenant(tenantID, 10<<30, 0)

	hash1 := "aabb000000000000000000000000000000000000000000000000000000000001"
	hash2 := "aabb000000000000000000000000000000000000000000000000000000000002"
	store.SeedCASBlock(hash1)
	store.SeedCASBlock(hash2)

	_, mux := newTestServer(t, store, "tok123")
	body := mustJSON(InitiateRequest{
		TenantID:  tenantID.String(),
		FileName:  "cached.bin",
		TotalSize: 2048,
		Chunks: []ChunkInfo{
			{BlockHash: hash1, SizeBytes: 1024},
			{BlockHash: hash2, SizeBytes: 1024},
		},
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
	if len(resp.UploadURLs) != 0 {
		t.Fatalf("expected 0 upload URLs (all in CAS), got %d", len(resp.UploadURLs))
	}
}
