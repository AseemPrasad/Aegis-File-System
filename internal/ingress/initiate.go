package ingress

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// handleInitiate processes POST /api/v1/ingest/initiate.
//
// Flow:
//  1. Validate tenant → quota check
//  2. Create/validate file node
//  3. Batch CAS dedup query
//  4. Mint pre-signed URLs for missing blocks
//  5. Create upload session
//  6. Return session + upload URLs
func (s *IngressServer) handleInitiate(w http.ResponseWriter, r *http.Request) error {
	start := time.Now()
	var req InitiateRequest
	if err := decodeJSON(r, &req); err != nil {
		s.metrics.InitiateTotal.WithLabelValues("400").Inc()
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if err := req.Validate(); err != nil {
		s.metrics.InitiateTotal.WithLabelValues("400").Inc()
		return err
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		s.metrics.InitiateTotal.WithLabelValues("400").Inc()
		return fmt.Errorf("%w: invalid tenant_id", ErrBadRequest)
	}

	ctx := r.Context()

	// 1. Quota check.
	quota, err := s.store.GetTenantQuota(ctx, tenantID)
	if err != nil {
		status := HTTPStatus(err)
		s.metrics.InitiateTotal.WithLabelValues(fmt.Sprintf("%d", status)).Inc()
		return err
	}
	if quota.StorageQuotaBytes > 0 && quota.UsedBytes+req.TotalSize > quota.StorageQuotaBytes {
		s.metrics.QuotaExceeded.Inc()
		s.metrics.InitiateTotal.WithLabelValues("402").Inc()
		return ErrQuotaExceeded
	}

	// 2. File node.
	var nodeID uuid.UUID
	if req.NodeID != nil {
		nodeID, err = uuid.Parse(*req.NodeID)
		if err != nil {
			s.metrics.InitiateTotal.WithLabelValues("400").Inc()
			return fmt.Errorf("%w: invalid node_id", ErrBadRequest)
		}
		if err := s.store.ValidateFileNode(ctx, tenantID, nodeID); err != nil {
			s.metrics.InitiateTotal.WithLabelValues(fmt.Sprintf("%d", HTTPStatus(err))).Inc()
			return err
		}
	} else {
		var parentID *uuid.UUID
		if req.ParentID != nil {
			pid, perr := uuid.Parse(*req.ParentID)
			if perr != nil {
				s.metrics.InitiateTotal.WithLabelValues("400").Inc()
				return fmt.Errorf("%w: invalid parent_id", ErrBadRequest)
			}
			parentID = &pid
		}
		nodeID, err = s.store.CreateFileNode(ctx, tenantID, parentID, req.FileName)
		if err != nil {
			s.metrics.InitiateTotal.WithLabelValues("500").Inc()
			return err
		}
	}

	// 3. Batch CAS dedup.
	hashes := make([][]byte, len(req.Chunks))
	for i, c := range req.Chunks {
		hashes[i] = MustDecodeHash(c.BlockHash)
	}
	existing, err := s.store.BatchQueryExistingCAS(ctx, hashes)
	if err != nil {
		s.metrics.InitiateTotal.WithLabelValues("503").Inc()
		return err
	}

	// 4. Compute missing and mint pre-signed URLs.
	var uploadURLs []UploadURL
	for _, c := range req.Chunks {
		if existing[c.BlockHash] {
			s.metrics.CASHitsTotal.Inc()
			continue
		}
		s.metrics.UploadsTotal.Inc()

		var url string
		var uerr error
		if s.blob != nil {
			url, uerr = s.blob.GenerateUploadURL(ctx, tenantID.String(), c.BlockHash, c.SizeBytes)
		} else {
			url, uerr = s.tokens.GeneratePreSignedURL(ctx, tenantID.String(), c.BlockHash, s.cfg.EndpointID)
		}
		if uerr != nil {
			s.metrics.InitiateTotal.WithLabelValues("500").Inc()
			return fmt.Errorf("mint pre-signed URL: %w", uerr)
		}
		uploadURLs = append(uploadURLs, UploadURL{
			BlockHash: c.BlockHash,
			URL:       url,
			SizeBytes: c.SizeBytes,
		})
	}

	// 5. Create upload session (expected_chunks = total, not just missing).
	clientIP := r.Header.Get("X-Forwarded-For")
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	ua := r.UserAgent()
	sessionID, expiresAt, err := s.store.CreateUploadSession(ctx, tenantID, nodeID, req.TotalSize, len(req.Chunks), clientIP, ua)
	if err != nil {
		s.metrics.InitiateTotal.WithLabelValues("500").Inc()
		return err
	}

	s.metrics.InitiateTotal.WithLabelValues("200").Inc()
	s.metrics.InitiateLatency.WithLabelValues("200").Observe(time.Since(start).Seconds())

	resp := InitiateResponse{
		SessionID:  sessionID.String(),
		NodeID:     nodeID.String(),
		ExpiresAt:  expiresAt.UTC().Format(time.RFC3339),
		UploadURLs: uploadURLs,
	}
	s.writeJSON(w, http.StatusOK, resp)
	return nil
}
