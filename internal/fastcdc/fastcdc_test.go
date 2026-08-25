package fastcdc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Determinism & correctness
// ---------------------------------------------------------------------------

func TestChunkAllDeterministic(t *testing.T) {
	data := []byte(strings.Repeat("Hello, Aegis! ", 10000))

	chunks1 := chunkBytes(t, data)
	chunks2 := chunkBytes(t, data)

	if len(chunks1) != len(chunks2) {
		t.Fatalf("chunk count mismatch: %d vs %d", len(chunks1), len(chunks2))
	}
	for i := range chunks1 {
		if chunks1[i].HashHex != chunks2[i].HashHex {
			t.Errorf("chunk %d hash mismatch: %s vs %s", i, chunks1[i].HashHex, chunks2[i].HashHex)
		}
		if chunks1[i].Offset != chunks2[i].Offset {
			t.Errorf("chunk %d offset mismatch: %d vs %d", i, chunks1[i].Offset, chunks2[i].Offset)
		}
		if chunks1[i].Size != chunks2[i].Size {
			t.Errorf("chunk %d size mismatch: %d vs %d", i, chunks1[i].Size, chunks2[i].Size)
		}
	}
}

func TestChunkHashMatchesContent(t *testing.T) {
	data := []byte("FastCDC content-defined chunking test data " + strings.Repeat("ABCD", 20000))
	chunks := chunkBytes(t, data)

	for i, ch := range chunks {
		expectedHash := sha256.Sum256(data[ch.Offset : ch.Offset+int64(ch.Size)])
		expectedHex := hex.EncodeToString(expectedHash[:])
		if ch.HashHex != expectedHex {
			t.Errorf("chunk %d: hash mismatch\n  got:  %s\n  want: %s", i, ch.HashHex, expectedHex)
		}
	}
}

// ---------------------------------------------------------------------------
// Min/Max bounds
// ---------------------------------------------------------------------------

func TestChunkRespectsMinSize(t *testing.T) {
	// Large random-ish data to ensure we get real chunks.
	data := make([]byte, 1024*1024) // 1 MiB
	for i := range data {
		data[i] = byte(i * 7) // pseudo-random
	}
	chunks := chunkBytes(t, data)

	for i, ch := range chunks {
		// Last chunk is allowed to be smaller than min.
		if i == len(chunks)-1 {
			if ch.Size <= 0 {
				t.Errorf("chunk %d has zero size", i)
			}
			continue
		}
		if ch.Size < MinChunk {
			t.Errorf("chunk %d size %d below minimum %d", i, ch.Size, MinChunk)
		}
	}
}

func TestChunkRespectsMaxSize(t *testing.T) {
	data := make([]byte, 16*1024*1024) // 16 MiB
	for i := range data {
		data[i] = byte(i * 3)
	}
	chunks := chunkBytes(t, data)

	for i, ch := range chunks {
		if ch.Size > MaxChunk {
			t.Errorf("chunk %d size %d exceeds maximum %d", i, ch.Size, MaxChunk)
		}
	}
}

// ---------------------------------------------------------------------------
// Coverage: chunks must cover entire input
// ---------------------------------------------------------------------------

func TestChunksCoverEntireInput(t *testing.T) {
	data := make([]byte, 5*1024*1024) // 5 MiB
	for i := range data {
		data[i] = byte(i ^ (i >> 8))
	}
	chunks := chunkBytes(t, data)

	totalCovered := 0
	for i, ch := range chunks {
		if ch.Offset < 0 {
			t.Errorf("chunk %d negative offset: %d", i, ch.Offset)
		}
		if i > 0 && ch.Offset != chunks[i-1].Offset+int64(chunks[i-1].Size) {
			t.Errorf("gap or overlap at chunk %d: prev_end=%d, start=%d",
				i, chunks[i-1].Offset+int64(chunks[i-1].Size), ch.Offset)
		}
		totalCovered += ch.Size
	}
	if totalCovered != len(data) {
		t.Errorf("total covered %d != input size %d", totalCovered, len(data))
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEmptyInput(t *testing.T) {
	chunks := chunkBytes(t, []byte{})
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}

func TestSmallInput(t *testing.T) {
	// Below minimum chunk size — should produce exactly one chunk.
	data := []byte("small")
	chunks := chunkBytes(t, data)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for small input, got %d", len(chunks))
	}
	if chunks[0].Size != len(data) {
		t.Errorf("chunk size %d != input size %d", chunks[0].Size, len(data))
	}
}

func TestExactlyMinSize(t *testing.T) {
	data := make([]byte, MinChunk)
	for i := range data {
		data[i] = byte(i)
	}
	chunks := chunkBytes(t, data)
	// Should produce at least one chunk, possibly split.
	total := 0
	for _, ch := range chunks {
		total += ch.Size
	}
	if total != len(data) {
		t.Errorf("total %d != input %d", total, len(data))
	}
}

func TestExactlyMaxSize(t *testing.T) {
	data := make([]byte, MaxChunk)
	for i := range data {
		data[i] = byte(i * 13)
	}
	chunks := chunkBytes(t, data)
	if len(chunks) < 1 {
		t.Fatal("expected at least 1 chunk")
	}
	total := 0
	for _, ch := range chunks {
		total += ch.Size
	}
	if total != MaxChunk {
		t.Errorf("total %d != MaxChunk %d", total, MaxChunk)
	}
}

// ---------------------------------------------------------------------------
// ChunkFromBytes
// ---------------------------------------------------------------------------

func TestChunkFromBytes(t *testing.T) {
	data := []byte(strings.Repeat("test ", 50000))
	chunks, err := ChunkFromBytes(data)
	if err != nil {
		t.Fatalf("ChunkFromBytes: %v", err)
	}
	total := 0
	for _, ch := range chunks {
		total += ch.Size
	}
	if total != len(data) {
		t.Errorf("total %d != input %d", total, len(data))
	}
}

// ---------------------------------------------------------------------------
// Streaming reader
// ---------------------------------------------------------------------------

func TestChunkerWithReader(t *testing.T) {
	data := []byte(strings.Repeat("streaming ", 80000))
	r := bytes.NewReader(data)
	chunker := New(r)

	var all []Chunk
	for {
		ch, err := chunker.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextChunk: %v", err)
		}
		all = append(all, *ch)
	}

	total := 0
	for _, ch := range all {
		total += ch.Size
	}
	if total != len(data) {
		t.Errorf("total %d != input %d", total, len(data))
	}
}

// ---------------------------------------------------------------------------
// SHA-256 helpers
// ---------------------------------------------------------------------------

func TestComputeFileSHA256(t *testing.T) {
	data := []byte("compute this hash")
	hash := ComputeFileSHA256(data)
	expected := sha256.Sum256(data)
	expectedHex := hex.EncodeToString(expected[:])
	if hash != expectedHex {
		t.Errorf("got %s, want %s", hash, expectedHex)
	}
}

func TestComputeFileSHA256FromReader(t *testing.T) {
	data := []byte("reader hash test")
	r := bytes.NewReader(data)
	hash, err := ComputeFileSHA256FromReader(r)
	if err != nil {
		t.Fatalf("ComputeFileSHA256FromReader: %v", err)
	}
	expected := sha256.Sum256(data)
	expectedHex := hex.EncodeToString(expected[:])
	if hash != expectedHex {
		t.Errorf("got %s, want %s", hash, expectedHex)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func chunkBytes(t *testing.T, data []byte) []Chunk {
	t.Helper()
	r := bytes.NewReader(data)
	chunker := New(r)
	chunks, err := chunker.ChunkAll()
	if err != nil {
		t.Fatalf("ChunkAll: %v", err)
	}
	return chunks
}
