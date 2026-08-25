package ingress

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// handleCommit processes POST /api/v1/ingest/commit.
//
// Flow:
//  1. Validate session + ownership
//  2. Atomic transaction: lock → version → CAS ensure → manifest → complete
//  3. Publish CDC event
//  4. Return version_id + version_number
func (s *IngressServer) handleCommit(w http.ResponseWriter, r *http.Request) error {
	start := time.Now()
	var req CommitRequest
	if err := decodeJSON(r, &req); err != nil {
		s.metrics.CommitTotal.WithLabelValues("400").Inc()
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if err := req.Validate(); err != nil {
		s.metrics.CommitTotal.WithLabelValues("400").Inc()
		return err
	}

	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		s.metrics.CommitTotal.WithLabelValues("400").Inc()
		return fmt.Errorf("%w: invalid session_id", ErrBadRequest)
	}

	contentSHA256, err := hex.DecodeString(req.ContentSHA256)
	if err != nil {
		s.metrics.CommitTotal.WithLabelValues("400").Inc()
		return fmt.Errorf("%w: invalid content_sha256", ErrBadRequest)
	}

	ctx := r.Context()

	// Pre-flight: get session to extract tenant_id (needed for commit + auth).
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		status := HTTPStatus(err)
		s.metrics.CommitTotal.WithLabelValues(fmt.Sprintf("%d", status)).Inc()
		return err
	}

	// Validate session isn't already completed or expired before entering tx.
	if session.Status == "COMPLETED" {
		s.metrics.CommitTotal.WithLabelValues("409").Inc()
		return ErrSessionCompleted
	}
	if time.Now().After(session.ExpiresAt) {
		s.metrics.CommitTotal.WithLabelValues("410").Inc()
		return ErrSessionExpired
	}

	tenantID, err := uuid.Parse(session.TenantID)
	if err != nil {
		s.metrics.CommitTotal.WithLabelValues("500").Inc()
		return fmt.Errorf("invalid tenant in session: %w", err)
	}

	// Atomic commit via store.
	versionID, versionNumber, cerr := s.store.CommitFile(ctx, tenantID, sessionID, contentSHA256, req.Blocks)
	if cerr != nil {
		status := HTTPStatus(cerr)
		s.metrics.CommitTotal.WithLabelValues(fmt.Sprintf("%d", status)).Inc()
		return cerr
	}

	// Post-commit: bump cache generation (best-effort, never fails the commit).
	_ = s.store.BumpCacheGeneration(ctx, tenantID)

	// Post-commit: verify blocks via ETag (best-effort, marks verified in DB).
	if s.blob != nil {
		for _, b := range req.Blocks {
			if verr := s.blob.VerifyBlock(ctx, b.BlockHash, b.ETag); verr != nil {
				s.logger.Warn("block verification failed", "hash", b.BlockHash, "err", verr)
				continue
			}
			if derr := s.store.MarkBlockVerified(ctx, MustDecodeHash(b.BlockHash)); derr != nil {
				s.logger.Warn("mark block verified failed", "hash", b.BlockHash, "err", derr)
			}
		}
	}

	// Post-commit: publish CDC event.
	if s.events != nil {
		blockHashes := make([]string, len(req.Blocks))
		chunks := make([]ChunkDetail, len(req.Blocks))
		for i, b := range req.Blocks {
			blockHashes[i] = b.BlockHash
			chunks[i] = ChunkDetail{
				Index:       b.ChunkIndex,
				BlockHash:   b.BlockHash,
				SizeBytes:   b.SizeBytes,
				OffsetBytes: b.Offset,
			}
		}
		mimeType := req.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		_ = s.events.PublishFileCommitted(ctx, FileCommittedEvent{
			EventID:       uuid.New().String(),
			EventType:     "VERSION_COMMITTED",
			TenantID:      tenantID.String(),
			NodeID:        session.NodeID,
			VersionID:     versionID,
			VersionNumber: versionNumber,
			TotalSize:     session.TotalSize,
			MimeType:      mimeType,
			ContentSHA256: req.ContentSHA256,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339),
			Chunks:        chunks,
			BlockHashes:   blockHashes,
		})
	}

	s.metrics.CommitTotal.WithLabelValues("201").Inc()
	s.metrics.CommitLatency.WithLabelValues("201").Observe(time.Since(start).Seconds())

	resp := CommitResponse{
		VersionID:     versionID,
		VersionNumber: versionNumber,
	}
	s.writeJSON(w, http.StatusCreated, resp)
	return nil
}
