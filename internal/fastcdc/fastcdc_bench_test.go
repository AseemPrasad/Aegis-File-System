package fastcdc

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// ---------------------------------------------------------------------------
// Benchmarks — FastCDC chunking performance
// ---------------------------------------------------------------------------

func BenchmarkChunkAll_1KB(b *testing.B) {
	data := make([]byte, 1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkAll_64KB(b *testing.B) {
	data := make([]byte, 64*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkAll_1MB(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkAll_10MB(b *testing.B) {
	data := make([]byte, 10*1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkAll_100MB(b *testing.B) {
	data := make([]byte, 100*1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkFromBytes_1MB(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ChunkFromBytes(data)
	}
}

func BenchmarkComputeFileSHA256_1MB(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeFileSHA256(data)
	}
}

func BenchmarkNextChunk_1MB(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		for {
			_, err := chunker.NextChunk()
			if err != nil {
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmarks — Content sensitivity (how much does data change affect chunks)
// ---------------------------------------------------------------------------

func BenchmarkChunkAll_RandomData(b *testing.B) {
	data := make([]byte, 1024*1024)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkAll_RepeatedData(b *testing.B) {
	data := bytes.Repeat([]byte{0xAB}, 1024*1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}

func BenchmarkChunkAll_Zeros(b *testing.B) {
	data := make([]byte, 1024*1024) // all zeros
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunker := New(r)
		chunker.ChunkAll()
	}
}
