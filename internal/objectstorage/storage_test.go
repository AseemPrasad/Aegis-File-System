package objectstorage

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FakeObjectStorage — in-memory test double implementing ObjectStorageClient
// ---------------------------------------------------------------------------

type fakeBlock struct {
	data      []byte
	etag      string
	size      int64
	tier      StorageTier
	createdAt time.Time
	verified  bool
}

// FakeObjectStorage is an in-memory ObjectStorageClient for unit tests.
type FakeObjectStorage struct {
	mu           sync.Mutex
	blocks       map[string]*fakeBlock
	generateErr  error
	verifyErr    error
	metadataErr  error
	deleteErr    error
	lifecycleErr error
	callLog      []string
}

var _ ObjectStorageClient = (*FakeObjectStorage)(nil)

func NewFakeObjectStorage() *FakeObjectStorage {
	return &FakeObjectStorage{blocks: make(map[string]*fakeBlock)}
}

func (f *FakeObjectStorage) SeedBlock(hash string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := md5.Sum(data)
	f.blocks[hash] = &fakeBlock{
		data:      data,
		etag:      fmt.Sprintf("%x", h),
		size:      int64(len(data)),
		tier:      TierHot,
		createdAt: time.Now(),
	}
}

func (f *FakeObjectStorage) SetGenerateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generateErr = err
}
func (f *FakeObjectStorage) SetVerifyErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyErr = err
}
func (f *FakeObjectStorage) SetDeleteErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteErr = err
}

func (f *FakeObjectStorage) CallLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.callLog))
	copy(out, f.callLog)
	return out
}

func (f *FakeObjectStorage) GenerateUploadURL(_ context.Context, tenantID, blockHash string, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "GenerateUploadURL:"+blockHash)
	if f.generateErr != nil {
		return "", f.generateErr
	}
	url := fmt.Sprintf("https://blob.example.com/%s/%s?tenant=%s", tenantID, blockHash, tenantID)
	return url, nil
}

func (f *FakeObjectStorage) VerifyBlock(_ context.Context, blockHash, expectedETag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "VerifyBlock:"+blockHash)
	if f.verifyErr != nil {
		return f.verifyErr
	}
	b, ok := f.blocks[blockHash]
	if !ok {
		return fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
	}
	if b.etag != expectedETag {
		return fmt.Errorf("%w: stored=%s expected=%s", ErrETagMismatch, b.etag, expectedETag)
	}
	b.verified = true
	return nil
}

func (f *FakeObjectStorage) GetBlockMetadata(_ context.Context, blockHash string) (*BlockMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "GetBlockMetadata:"+blockHash)
	if f.metadataErr != nil {
		return nil, f.metadataErr
	}
	b, ok := f.blocks[blockHash]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
	}
	return &BlockMetadata{
		BlockHash:    blockHash,
		ETag:         b.etag,
		SizeBytes:    b.size,
		StorageTier:  b.tier,
		LastModified: b.createdAt,
		IsVerified:   b.verified,
	}, nil
}

func (f *FakeObjectStorage) GetBlockReader(_ context.Context, blockHash string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "GetBlockReader:"+blockHash)
	b, ok := f.blocks[blockHash]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
	}
	return io.NopCloser(bytes.NewReader(b.data)), b.size, nil
}

func (f *FakeObjectStorage) DeleteBlock(_ context.Context, blockHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "DeleteBlock:"+blockHash)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.blocks, blockHash)
	return nil
}

func (f *FakeObjectStorage) ApplyLifecycle(_ context.Context, _ LifecyclePolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "ApplyLifecycle")
	return f.lifecycleErr
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDefaultLifecyclePolicy(t *testing.T) {
	p := DefaultLifecyclePolicy()
	if len(p.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(p.Rules))
	}
	if p.Rules[0].ID != "tier-warm" || p.Rules[0].TransitionDay != 7 || p.Rules[0].TargetTier != TierWarm {
		t.Errorf("rule 0 mismatch: %+v", p.Rules[0])
	}
	if p.Rules[1].ID != "tier-cold" || p.Rules[1].TransitionDay != 30 || p.Rules[1].TargetTier != TierCold {
		t.Errorf("rule 1 mismatch: %+v", p.Rules[1])
	}
	if p.AbortIncompleteMultipartDays != 1 {
		t.Errorf("expected abort=1, got %d", p.AbortIncompleteMultipartDays)
	}
	if p.NoncurrentVersionExpirationDays != 1 {
		t.Errorf("expected noncurrent expiry=1, got %d", p.NoncurrentVersionExpirationDays)
	}
}

func TestFakeStorageGenerateUploadURL(t *testing.T) {
	f := NewFakeObjectStorage()
	ctx := context.Background()

	url, err := f.GenerateUploadURL(ctx, "tenant-1", "abc123", 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(url, "abc123") {
		t.Errorf("URL should contain block hash, got: %s", url)
	}
	if !strings.Contains(url, "tenant-1") {
		t.Errorf("URL should contain tenant ID, got: %s", url)
	}
}

func TestFakeStorageGenerateUploadURL_Error(t *testing.T) {
	f := NewFakeObjectStorage()
	f.SetGenerateErr(ErrStorageUnavailable)

	_, err := f.GenerateUploadURL(context.Background(), "t", "h", 100)
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Errorf("expected ErrStorageUnavailable, got: %v", err)
	}
}

func TestFakeStorageVerifyBlock_Success(t *testing.T) {
	f := NewFakeObjectStorage()
	data := []byte("hello world")
	h := md5.Sum(data)
	etag := fmt.Sprintf("%x", h)
	f.SeedBlock("block1", data)

	err := f.VerifyBlock(context.Background(), "block1", etag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, _ := f.GetBlockMetadata(context.Background(), "block1")
	if !meta.IsVerified {
		t.Error("expected IsVerified=true after successful VerifyBlock")
	}
}

func TestFakeStorageVerifyBlock_NotFound(t *testing.T) {
	f := NewFakeObjectStorage()
	err := f.VerifyBlock(context.Background(), "missing", "etag")
	if !errors.Is(err, ErrBlockNotFound) {
		t.Errorf("expected ErrBlockNotFound, got: %v", err)
	}
}

func TestFakeStorageVerifyBlock_ETagMismatch(t *testing.T) {
	f := NewFakeObjectStorage()
	f.SeedBlock("block1", []byte("data"))

	err := f.VerifyBlock(context.Background(), "block1", "wrong-etag")
	if !errors.Is(err, ErrETagMismatch) {
		t.Errorf("expected ErrETagMismatch, got: %v", err)
	}
}

func TestFakeStorageGetBlockMetadata(t *testing.T) {
	f := NewFakeObjectStorage()
	data := []byte("test data")
	f.SeedBlock("block1", data)

	meta, err := f.GetBlockMetadata(context.Background(), "block1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.BlockHash != "block1" {
		t.Errorf("expected hash block1, got %s", meta.BlockHash)
	}
	if meta.SizeBytes != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), meta.SizeBytes)
	}
	if meta.StorageTier != TierHot {
		t.Errorf("expected tier HOT, got %s", meta.StorageTier)
	}
}

func TestFakeStorageGetBlockReader(t *testing.T) {
	f := NewFakeObjectStorage()
	data := []byte("read me")
	f.SeedBlock("block1", data)

	reader, size, err := f.GetBlockReader(context.Background(), "block1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("data mismatch: got %s, want %s", got, data)
	}
	if size != int64(len(data)) {
		t.Errorf("size mismatch: got %d, want %d", size, len(data))
	}
}

func TestFakeStorageDeleteBlock(t *testing.T) {
	f := NewFakeObjectStorage()
	f.SeedBlock("block1", []byte("data"))

	err := f.DeleteBlock(context.Background(), "block1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = f.GetBlockMetadata(context.Background(), "block1")
	if !errors.Is(err, ErrBlockNotFound) {
		t.Errorf("expected ErrBlockNotFound after delete, got: %v", err)
	}
}

func TestFakeStorageDeleteBlock_Error(t *testing.T) {
	f := NewFakeObjectStorage()
	f.SeedBlock("block1", []byte("data"))
	f.SetDeleteErr(ErrDeleteFailed)

	err := f.DeleteBlock(context.Background(), "block1")
	if !errors.Is(err, ErrDeleteFailed) {
		t.Errorf("expected ErrDeleteFailed, got: %v", err)
	}
}

func TestFakeStorageApplyLifecycle(t *testing.T) {
	f := NewFakeObjectStorage()
	p := DefaultLifecyclePolicy()

	err := f.ApplyLifecycle(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	log := f.CallLog()
	if len(log) == 0 || log[len(log)-1] != "ApplyLifecycle" {
		t.Errorf("expected ApplyLifecycle in call log, got: %v", log)
	}
}

func TestFakeStorageCallLog(t *testing.T) {
	f := NewFakeObjectStorage()
	f.SeedBlock("b1", []byte("x"))
	ctx := context.Background()

	f.GenerateUploadURL(ctx, "t", "b1", 1)
	f.VerifyBlock(ctx, "b1", "wrong")
	f.GetBlockMetadata(ctx, "b1")

	log := f.CallLog()
	if len(log) != 3 {
		t.Fatalf("expected 3 log entries, got %d: %v", len(log), log)
	}
	if log[0] != "GenerateUploadURL:b1" {
		t.Errorf("log[0] = %s", log[0])
	}
	if log[1] != "VerifyBlock:b1" {
		t.Errorf("log[1] = %s", log[1])
	}
	if log[2] != "GetBlockMetadata:b1" {
		t.Errorf("log[2] = %s", log[2])
	}
}

func TestStorageTierConstants(t *testing.T) {
	if TierHot != "HOT" {
		t.Errorf("TierHot = %q", TierHot)
	}
	if TierWarm != "WARM" {
		t.Errorf("TierWarm = %q", TierWarm)
	}
	if TierCold != "COLD" {
		t.Errorf("TierCold = %q", TierCold)
	}
}

func TestErrorSentinels(t *testing.T) {
	sentinels := []error{
		ErrBlockNotFound,
		ErrETagMismatch,
		ErrUploadFailed,
		ErrDeleteFailed,
		ErrStorageUnavailable,
	}
	seen := make(map[string]bool)
	for _, s := range sentinels {
		if s == nil {
			t.Fatal("nil sentinel")
		}
		if seen[s.Error()] {
			t.Errorf("duplicate error message: %s", s.Error())
		}
		seen[s.Error()] = true
	}
}
