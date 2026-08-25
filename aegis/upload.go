package aegis

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegis-dev/aegis/internal/fastcdc"
)

// ---------------------------------------------------------------------------
// Parallel chunk upload with retry and progress reporting
// ---------------------------------------------------------------------------

const (
	defaultMaxParallel = 10
	defaultMaxRetries  = 5
	defaultBaseDelay   = 1 * time.Second
	defaultMaxDelay    = 16 * time.Second
)

// uploadState tracks progress across goroutines.
type uploadState struct {
	mu            sync.Mutex
	uploadedBytes int64
	totalBytes    int64
	startTime     time.Time
	progressCb    func(UploadProgress)
}

func (s *uploadState) chunkComplete(size int64) {
	s.mu.Lock()
	s.uploadedBytes += size
	uploaded := s.uploadedBytes
	total := s.totalBytes
	elapsed := time.Since(s.startTime).Seconds()
	s.mu.Unlock()

	if s.progressCb == nil || total == 0 {
		return
	}

	percent := float64(uploaded) / float64(total) * 100
	var bitrate float64
	var eta time.Duration
	if elapsed > 0 {
		bitrate = float64(uploaded) / elapsed
		remaining := float64(total-uploaded) / bitrate
		eta = time.Duration(remaining * float64(time.Second))
	}

	s.progressCb(UploadProgress{
		BytesUploaded:          uploaded,
		TotalBytes:             total,
		PercentComplete:        percent,
		EstimatedTimeRemaining: eta,
		CurrentBitrate:         bitrate,
	})
}

// uploadChunksParallel uploads missing chunks concurrently with retry.
func (c *Client) uploadChunksParallel(
	ctx context.Context,
	file *os.File,
	allChunks []fastcdc.Chunk,
	urlMap map[string]UploadURL,
	opts UploadOptions,
	totalSize int64,
) (int64, error) {
	state := &uploadState{
		totalBytes: totalSize,
		startTime:  time.Now(),
		progressCb: opts.ProgressCallback,
	}

	// Build work list: only chunks that have upload URLs.
	type workItem struct {
		chunk fastcdc.Chunk
		url   UploadURL
	}
	var work []workItem
	for _, ch := range allChunks {
		if u, ok := urlMap[ch.HashHex]; ok {
			work = append(work, workItem{chunk: ch, url: u})
		}
	}

	if len(work) == 0 {
		return 0, nil
	}

	semaphore := make(chan struct{}, defaultMaxParallel)
	errChan := make(chan error, len(work))
	var wg sync.WaitGroup
	var totalUploaded int64

	for _, w := range work {
		wg.Add(1)
		go func(item workItem) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Read chunk data from file by seeking to offset.
			data, err := readChunkFromFile(file, item.chunk)
			if err != nil {
				errChan <- fmt.Errorf("chunk %s read: %w", item.chunk.HashHex[:12], err)
				return
			}

			// Upload with retry.
			err = c.uploadChunkWithRetry(ctx, item.url.URL, data, item.chunk)
			if err != nil {
				errChan <- fmt.Errorf("chunk %s: %w", item.chunk.HashHex[:12], err)
				return
			}

			state.chunkComplete(int64(item.chunk.Size))

			if opts.OnChunkComplete != nil {
				opts.OnChunkComplete(ChunkInfo{
					BlockHash: item.chunk.HashHex,
					SizeBytes: int64(item.chunk.Size),
				})
			}

			atomic.AddInt64(&totalUploaded, int64(item.chunk.Size))
			errChan <- nil
		}(w)
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	for err := range errChan {
		if err != nil {
			return atomic.LoadInt64(&totalUploaded), err
		}
	}
	return atomic.LoadInt64(&totalUploaded), nil
}

// readChunkFromFile seeks to the chunk's offset and reads its data.
func readChunkFromFile(file *os.File, chunk fastcdc.Chunk) ([]byte, error) {
	buf := make([]byte, chunk.Size)
	n, err := file.ReadAt(buf, chunk.Offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("ReadAt offset=%d size=%d: %w", chunk.Offset, chunk.Size, err)
	}
	if n < chunk.Size {
		return buf[:n], nil // last chunk may be smaller
	}
	return buf, nil
}

// uploadChunkWithRetry uploads a single chunk with exponential backoff.
func (c *Client) uploadChunkWithRetry(ctx context.Context, url string, data []byte, chunk fastcdc.Chunk) error {
	var lastErr error

	for attempt := 0; attempt < defaultMaxRetries; attempt++ {
		err := c.putChunk(ctx, url, data)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isTransientError(err) {
			return err
		}

		delay := backoffDelay(attempt)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return fmt.Errorf("failed after %d retries: %w", defaultMaxRetries, lastErr)
}

// putChunk performs a single PUT request for one chunk.
func (c *Client) putChunk(ctx context.Context, url string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))
	req.ContentLength = int64(len(data))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http put: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d uploading chunk", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func backoffDelay(attempt int) time.Duration {
	delay := float64(defaultBaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(defaultMaxDelay) {
		delay = float64(defaultMaxDelay)
	}
	return time.Duration(delay)
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transientPatterns := []string{
		"timeout", "connection reset", "connection refused",
		"broken pipe", "EOF", "i/o timeout",
		"HTTP 502", "HTTP 503", "HTTP 504",
	}
	for _, p := range transientPatterns {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(p)) {
			return true
		}
	}
	return false
}
