package fastcdc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// Fuzz tests — Go native fuzzing (go test -fuzz=...)
// ---------------------------------------------------------------------------

// FuzzChunkAllDeterministic verifies that chunking the same input always
// produces the same output, regardless of input shape.
func FuzzChunkAllDeterministic(f *testing.F) {
	// Seed corpus: empty, single byte, min-size, avg-size, max-size, random.
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(make([]byte, MinChunk))
	f.Add(make([]byte, AvgChunk))
	f.Add(make([]byte, MaxChunk))
	f.Add([]byte("Hello, Aegis! FastCDC fuzz testing."))

	// Add seeds with interesting patterns.
	seed := make([]byte, 1024)
	for i := range seed {
		seed[i] = byte(i ^ (i >> 3))
	}
	f.Add(seed)

	f.Fuzz(func(t *testing.T, data []byte) {
		chunks1 := fuzzChunkAll(t, data)
		chunks2 := fuzzChunkAll(t, data)

		if len(chunks1) != len(chunks2) {
			t.Fatalf("non-deterministic: chunk count %d vs %d", len(chunks1), len(chunks2))
		}
		for i := range chunks1 {
			if chunks1[i].HashHex != chunks2[i].HashHex {
				t.Fatalf("non-deterministic: chunk %d hash mismatch", i)
			}
			if chunks1[i].Offset != chunks2[i].Offset {
				t.Fatalf("non-deterministic: chunk %d offset mismatch", i)
			}
			if chunks1[i].Size != chunks2[i].Size {
				t.Fatalf("non-deterministic: chunk %d size mismatch", i)
			}
		}
	})
}

// FuzzChunkAllCoversInput verifies that chunks always cover the entire input.
func FuzzChunkAllCoversInput(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{42})
	f.Add(make([]byte, MinChunk*2))
	f.Add(make([]byte, AvgChunk+1))

	f.Fuzz(func(t *testing.T, data []byte) {
		chunks := fuzzChunkAll(t, data)

		total := 0
		for i, ch := range chunks {
			if ch.Size <= 0 {
				t.Fatalf("chunk %d has non-positive size %d", i, ch.Size)
			}
			if ch.Offset < 0 {
				t.Fatalf("chunk %d has negative offset %d", i, ch.Offset)
			}
			if i > 0 {
				prevEnd := chunks[i-1].Offset + int64(chunks[i-1].Size)
				if ch.Offset != prevEnd {
					t.Fatalf("gap/overlap at chunk %d: prev_end=%d, start=%d",
						i, prevEnd, ch.Offset)
				}
			}
			total += ch.Size
		}
		if total != len(data) {
			t.Fatalf("total covered %d != input size %d", total, len(data))
		}
	})
}

// FuzzChunkAllHashIntegrity verifies SHA-256 hashes match actual chunk content.
func FuzzChunkAllHashIntegrity(f *testing.F) {
	f.Add([]byte("test"))
	f.Add(make([]byte, MinChunk))
	f.Add(make([]byte, AvgChunk))

	f.Fuzz(func(t *testing.T, data []byte) {
		chunks := fuzzChunkAll(t, data)

		for i, ch := range chunks {
			end := ch.Offset + int64(ch.Size)
			if end > int64(len(data)) {
				t.Fatalf("chunk %d: end %d > data len %d", i, end, len(data))
			}
			expectedHash := sha256.Sum256(data[ch.Offset:end])
			expectedHex := hex.EncodeToString(expectedHash[:])
			if ch.HashHex != expectedHex {
				t.Fatalf("chunk %d: hash mismatch\n  got:  %s\n  want: %s",
					i, ch.HashHex, expectedHex)
			}
		}
	})
}

// FuzzChunkAllMinMaxBounds verifies chunk size stays within configured bounds.
func FuzzChunkAllMinMaxBounds(f *testing.F) {
	f.Add(make([]byte, 1024))
	f.Add(make([]byte, MinChunk))
	f.Add(make([]byte, AvgChunk))
	f.Add(make([]byte, MaxChunk))
	f.Add(make([]byte, MaxChunk*3))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		chunks := fuzzChunkAll(t, data)
		if len(chunks) == 0 {
			return
		}

		for i, ch := range chunks {
			// Last chunk is allowed to be smaller than MinChunk.
			if i == len(chunks)-1 {
				if ch.Size <= 0 {
					t.Fatalf("last chunk has non-positive size")
				}
				continue
			}
			if ch.Size < MinChunk {
				t.Fatalf("chunk %d size %d below minimum %d", i, ch.Size, MinChunk)
			}
			if ch.Size > MaxChunk {
				t.Fatalf("chunk %d size %d exceeds maximum %d", i, ch.Size, MaxChunk)
			}
		}
	})
}

// FuzzChunkerStreaming verifies that the streaming NextChunk API produces
// the same result as ChunkAll.
func FuzzChunkerStreaming(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("streaming test"))
	f.Add(make([]byte, AvgChunk))

	f.Fuzz(func(t *testing.T, data []byte) {
		chunksAll := fuzzChunkAll(t, data)

		r := bytes.NewReader(data)
		chunker := New(r)
		var chunksStream []Chunk
		for {
			ch, err := chunker.NextChunk()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextChunk: %v", err)
			}
			chunksStream = append(chunksStream, *ch)
		}

		if len(chunksAll) != len(chunksStream) {
			t.Fatalf("ChunkAll=%d chunks, streaming=%d",
				len(chunksAll), len(chunksStream))
		}
		for i := range chunksAll {
			if chunksAll[i].HashHex != chunksStream[i].HashHex {
				t.Fatalf("chunk %d hash mismatch between ChunkAll and streaming", i)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func fuzzChunkAll(t *testing.T, data []byte) []Chunk {
	t.Helper()
	r := bytes.NewReader(data)
	chunker := New(r)
	chunks, err := chunker.ChunkAll()
	if err != nil {
		t.Fatalf("ChunkAll: %v", err)
	}
	return chunks
}
