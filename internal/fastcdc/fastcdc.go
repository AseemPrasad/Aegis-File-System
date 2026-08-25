// Package fastcdc implements the FastCDC content-defined chunking algorithm
// in pure Go. It produces content-defined chunk boundaries using a Gear hash
// rolling fingerprint with normalized two-phase masking.
//
// Ported from the Rust implementation (crates/fastcdc/src/lib.rs).
// Reference: Wen et al., "FastCDC: A Fast and Efficient Content-Defined
// Chunking Algorithm for Data Deduplication," USENIX ATC '16.
package fastcdc

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
)

// ---------------------------------------------------------------------------
// Constants matching the Rust implementation.
// ---------------------------------------------------------------------------

const (
	// MinChunk is the minimum chunk size (64 KiB).
	MinChunk = 64 * 1024
	// AvgChunk is the target average chunk size (1 MiB).
	AvgChunk = 1 * 1024 * 1024
	// MaxChunk is the maximum chunk size (4 MiB).
	MaxChunk = 4 * 1024 * 1024

	// maskS is the sub-average phase mask (18 bits: 0x0003FFFF).
	maskS uint32 = 0x0003_FFFF
	// maskL is the post-average phase mask (19 bits: 0x0007FFFF).
	maskL uint32 = 0x0007_FFFF
)

// GearSeed is the pinned seed for generating the Gear hash lookup table.
const GearSeed uint64 = 0x6DCA4958_95636829

// ---------------------------------------------------------------------------
// Gear hash table (const-generated at init time)
// ---------------------------------------------------------------------------

var gearTable [256]uint32

func init() {
	gearTable = generateGearTable(GearSeed)
}

func generateGearTable(seed uint64) [256]uint32 {
	var table [256]uint32
	s := seed
	for i := 0; i < 256; i++ {
		s = (s >> 1) ^ (uint64(0xA0CC36C9) & (-(s & 1)))
		table[i] = uint32(s)
	}
	return table
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config controls chunking parameters.
type Config struct {
	MinChunk int
	AvgChunk int
	MaxChunk int
	MaskS    uint32
	MaskL    uint32
}

// DefaultConfig returns the standard 64K/1M/4M configuration.
func DefaultConfig() Config {
	return Config{
		MinChunk: MinChunk,
		AvgChunk: AvgChunk,
		MaxChunk: MaxChunk,
		MaskS:    maskS,
		MaskL:    maskL,
	}
}

// ---------------------------------------------------------------------------
// Chunk — output of the chunker
// ---------------------------------------------------------------------------

// Chunk is one content-defined chunk produced by the chunker.
type Chunk struct {
	HashHex string // lowercase hex-encoded SHA-256 (64 chars)
	Offset  int64  // byte offset within the input stream
	Size    int    // chunk size in bytes
}

// ---------------------------------------------------------------------------
// Chunker — streaming FastCDC chunker
// ---------------------------------------------------------------------------

// Chunker splits an io.Reader into content-defined chunks.
type Chunker struct {
	reader io.Reader
	cfg    Config

	buf     []byte // internal buffer for partial reads
	bufLen  int
	offset  int64 // current byte position in stream
	hasher  hash.Hash
	fingerprint uint32 // rolling Gear hash
	cutPhase    bool   // true = in post-average phase

	// buffered result from previous Read that wasn't consumed
	pending []Chunk
}

// New creates a Chunker with default configuration.
func New(r io.Reader) *Chunker {
	return NewWithConfig(r, DefaultConfig())
}

// NewWithConfig creates a Chunker with explicit configuration.
func NewWithConfig(r io.Reader, cfg Config) *Chunker {
	if cfg.MinChunk <= 0 {
		cfg.MinChunk = MinChunk
	}
	if cfg.AvgChunk <= 0 {
		cfg.AvgChunk = AvgChunk
	}
	if cfg.MaxChunk <= 0 {
		cfg.MaxChunk = MaxChunk
	}
	if cfg.MaskS == 0 {
		cfg.MaskS = maskS
	}
	if cfg.MaskL == 0 {
		cfg.MaskL = maskL
	}
	return &Chunker{
		reader: r,
		cfg:    cfg,
		buf:    make([]byte, cfg.MaxChunk*2),
		hasher: sha256.New(),
	}
}

// NextChunk reads and returns the next chunk, or nil at EOF.
func (c *Chunker) NextChunk() (*Chunk, error) {
	// Return buffered chunk from previous call.
	if len(c.pending) > 0 {
		chunk := c.pending[0]
		c.pending = c.pending[1:]
		return &chunk, nil
	}

	chunks, err := c.readUntilCut()
	if err != nil && len(chunks) == 0 {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, io.EOF
	}
	if len(chunks) > 1 {
		c.pending = chunks[1:]
	}
	return &chunks[0], nil
}

// ChunkAll chunks the entire reader and returns all chunks.
func (c *Chunker) ChunkAll() ([]Chunk, error) {
	var all []Chunk
	for {
		chunk, err := c.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			return all, err
		}
		all = append(all, *chunk)
	}
	return all, nil
}

// readUntilCut reads data and finds the next chunk boundary.
func (c *Chunker) readUntilCut() ([]Chunk, error) {
	var chunks []Chunk
	startOffset := c.offset

	// Read up to MaxChunk bytes.
	n, readErr := io.ReadFull(c.reader, c.buf[c.bufLen:c.bufLen+c.cfg.MaxChunk])
	c.bufLen += n

	if c.bufLen == 0 {
		return chunks, readErr
	}

	// Process the buffer to find cut points.
	var cutPos int
	for cutPos < c.bufLen {
		remaining := c.bufLen - cutPos
		if remaining < c.cfg.MinChunk && readErr == nil {
			break
		}

		cutLen := c.findCut(c.buf[cutPos:c.bufLen], remaining)
		if cutLen == 0 {
			cutLen = remaining
		}

		c.hasher.Reset()
		c.hasher.Write(c.buf[cutPos : cutPos+cutLen])
		hashBytes := c.hasher.Sum(nil)

		chunks = append(chunks, Chunk{
			HashHex: hex.EncodeToString(hashBytes),
			Offset:  startOffset + int64(cutPos),
			Size:    cutLen,
		})

		c.offset += int64(cutLen)
		cutPos += cutLen
		c.fingerprint = 0
		c.cutPhase = false
	}

	// Shift consumed data to front of buffer.
	remaining := c.bufLen - cutPos
	if remaining > 0 && cutPos > 0 {
		copy(c.buf, c.buf[cutPos:c.bufLen])
	}
	c.bufLen = remaining

	if readErr != nil && c.bufLen > 0 {
		// Flush remaining data as final chunk.
		c.hasher.Reset()
		c.hasher.Write(c.buf[:c.bufLen])
		hashBytes := c.hasher.Sum(nil)
		chunks = append(chunks, Chunk{
			HashHex: hex.EncodeToString(hashBytes),
			Offset:  startOffset + int64(cutPos),
			Size:    c.bufLen,
		})
		c.offset += int64(c.bufLen)
		c.bufLen = 0
		// Return chunks without error so ChunkAll can accumulate them.
		// Next call will return (nil, io.EOF).
		return chunks, nil
	}

	return chunks, readErr
}

// findCut searches for a chunk boundary using normalized two-phase masking.
// Returns the chunk length to use (0 means no cut found).
// Only updates fingerprint/cutPhase — offset is managed by readUntilCut.
func (c *Chunker) findCut(data []byte, dataLen int) int {
	if dataLen <= 0 {
		return 0
	}

	norm := c.cfg.AvgChunk
	minChunk := c.cfg.MinChunk
	maxChunk := c.cfg.MaxChunk

	// Phase 1: Sub-average (small mask, fast scanning).
	fingerprint := c.fingerprint
	for i := 0; i < dataLen; i++ {
		fingerprint = (fingerprint << 1) + gearTable[data[i]]
		currentSize := i + 1

		if currentSize >= norm {
			// Switch to Phase 2.
			c.cutPhase = true
			break
		}

		if currentSize >= minChunk {
			if fingerprint&c.cfg.MaskS == 0 {
				c.fingerprint = fingerprint
				return currentSize
			}
		}
	}

	if !c.cutPhase {
		c.fingerprint = fingerprint
		return 0
	}

	// Phase 2: Post-average (larger mask, selective cutting).
	for i := 0; i < dataLen; i++ {
		fingerprint = (fingerprint << 1) + gearTable[data[i]]
		currentSize := i + 1

		if currentSize >= maxChunk {
			c.fingerprint = fingerprint
			return currentSize
		}

		if fingerprint&c.cfg.MaskL == 0 {
			c.fingerprint = fingerprint
			return currentSize
		}
	}

	c.fingerprint = fingerprint
	return 0
}

// ChunkFromBytes chunks a byte slice and returns all chunks.
func ChunkFromBytes(data []byte) ([]Chunk, error) {
	r := NewWithConfig(
		&byteReader{data: data, pos: 0},
		DefaultConfig(),
	)
	return r.ChunkAll()
}

// ---------------------------------------------------------------------------
// byteReader — adapter for byte slices
// ---------------------------------------------------------------------------

type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ComputeFileSHA256 computes the SHA-256 of the entire file content.
func ComputeFileSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ComputeFileSHA256FromReader computes SHA-256 by reading an io.Reader.
func ComputeFileSHA256FromReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EncodeHashBytes encodes a byte slice to lowercase hex.
func EncodeHashBytes(b []byte) string {
	return hex.EncodeToString(b)
}

// DecodeHashHex decodes a hex string to bytes.
func DecodeHashHex(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// ComputeSHA256Hex computes SHA-256 of data and returns lowercase hex.
func ComputeSHA256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Unused import guard.
var _ = binary.BigEndian
