package ingress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegis-dev/aegis/internal/database"
)

// ---------------------------------------------------------------------------
// Store interface — abstracts all database operations for test fakes.
// ---------------------------------------------------------------------------

// Store provides the data access layer for the ingestion engine.
// PgStore wraps *database.DatabaseClient; FakeStore is the test double.
type Store interface {
	Ping(ctx context.Context) error
	GetTenantQuota(ctx context.Context, tenantID uuid.UUID) (TenantQuota, error)
	CreateFileNode(ctx context.Context, tenantID uuid.UUID, parentID *uuid.UUID, name string) (uuid.UUID, error)
	ValidateFileNode(ctx context.Context, tenantID, nodeID uuid.UUID) error
	BatchQueryExistingCAS(ctx context.Context, hashes [][]byte) (map[string]bool, error)
	CreateUploadSession(ctx context.Context, tenantID, nodeID uuid.UUID, totalSize int64, chunks int, ip, ua string) (uuid.UUID, time.Time, error)
	GetSession(ctx context.Context, sessionID uuid.UUID) (SessionRecord, error)
	CommitFile(ctx context.Context, tenantID, sessionID uuid.UUID, contentSHA256 []byte, blocks []BlockMeta) (string, int, error)
	BumpCacheGeneration(ctx context.Context, tenantID uuid.UUID) error
	ExpireStaleSessions(ctx context.Context) (int64, error)
	MarkBlockVerified(ctx context.Context, blockHash []byte) error
}

// ---------------------------------------------------------------------------
// PgStore — production adapter wrapping DatabaseClient
// ---------------------------------------------------------------------------

// PgStore implements Store backed by *database.DatabaseClient.
type PgStore struct {
	db     *database.DatabaseClient
	logger *slog.Logger
}

func NewPgStore(db *database.DatabaseClient, lg *slog.Logger) *PgStore {
	if lg == nil {
		lg = slog.Default()
	}
	return &PgStore{db: db, logger: lg}
}

func (s *PgStore) Ping(ctx context.Context) error {
	tag, err := s.db.ExecWithMetrics(ctx, database.OpWrite, "ping", "", "SELECT 1")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDBUnavailable, err)
	}
	_ = tag
	return nil
}

func (s *PgStore) GetTenantQuota(ctx context.Context, tenantID uuid.UUID) (TenantQuota, error) {
	rows, err := s.db.QueryWithMetrics(ctx, database.OpMetadata, "get_tenant_quota", tenantID.String(),
		`SELECT storage_quota_bytes, used_bytes FROM tenants WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return TenantQuota{}, fmt.Errorf("%w: %v", ErrDBUnavailable, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return TenantQuota{}, ErrTenantNotFound
	}
	var q TenantQuota
	if err := rows.Scan(&q.StorageQuotaBytes, &q.UsedBytes); err != nil {
		return TenantQuota{}, err
	}
	return q, nil
}

func (s *PgStore) CreateFileNode(ctx context.Context, tenantID uuid.UUID, parentID *uuid.UUID, name string) (uuid.UUID, error) {
	tx, err := s.db.BeginWriteTx(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: begin tx for create node", ErrDBUnavailable)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var nodeID uuid.UUID
	if parentID != nil {
		err = tx.QueryRow(ctx,
			`INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
			 VALUES ($1, $2, $3, 'FILE') RETURNING node_id`,
			tenantID, parentID, name).Scan(&nodeID)
	} else {
		err = tx.QueryRow(ctx,
			`INSERT INTO namespace_nodes (tenant_id, parent_id, name, type)
			 VALUES ($1, NULL, $2, 'FILE') RETURNING node_id`,
			tenantID, name).Scan(&nodeID)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("create file node: %w", err)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return uuid.Nil, fmt.Errorf("commit create node: %w", cerr)
	}
	return nodeID, nil
}

func (s *PgStore) ValidateFileNode(ctx context.Context, tenantID, nodeID uuid.UUID) error {
	rows, err := s.db.QueryWithMetrics(ctx, database.OpMetadata, "validate_file_node", tenantID.String(),
		`SELECT type::text FROM namespace_nodes WHERE node_id = $1 AND tenant_id = $2`, nodeID, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return ErrNodeNotFound
	}
	var nodeType string
	if err := rows.Scan(&nodeType); err != nil {
		return err
	}
	if nodeType != "FILE" {
		return ErrNodeNotFile
	}
	return rows.Err()
}

func (s *PgStore) BatchQueryExistingCAS(ctx context.Context, hashes [][]byte) (map[string]bool, error) {
	if len(hashes) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := s.db.QueryWithMetrics(ctx, database.OpMetadata, "batch_cas_query", "",
		`SELECT block_hash FROM cas_blocks WHERE block_hash = ANY($1)`, hashes)
	if err != nil {
		return nil, fmt.Errorf("%w: batch CAS query: %v", ErrDBUnavailable, err)
	}
	defer rows.Close()
	existing := make(map[string]bool, len(hashes))
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		existing[fmt.Sprintf("%x", h)] = true
	}
	return existing, rows.Err()
}

func (s *PgStore) CreateUploadSession(ctx context.Context, tenantID, nodeID uuid.UUID, totalSize int64, chunks int, ip, ua string) (uuid.UUID, time.Time, error) {
	tx, err := s.db.BeginWriteTx(ctx)
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("%w: begin tx for create session", ErrDBUnavailable)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var sessionID uuid.UUID
	var expiresAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO upload_sessions
		     (tenant_id, node_id, total_size, expected_chunks, client_ip, user_agent)
		 VALUES ($1, $2, $3, $4, $5::inet, $6)
		 RETURNING session_id, expires_at`,
		tenantID, nodeID, totalSize, chunks, ip, ua,
	).Scan(&sessionID, &expiresAt)
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("create session: %w", err)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("commit create session: %w", cerr)
	}
	return sessionID, expiresAt, nil
}

func (s *PgStore) GetSession(ctx context.Context, sessionID uuid.UUID) (SessionRecord, error) {
	var rec SessionRecord
	rows, err := s.db.QueryWithMetrics(ctx, database.OpMetadata, "get_session", "",
		`SELECT session_id::text, tenant_id::text, COALESCE(node_id::text,''),
		        status, total_size, expected_chunks, expires_at
		 FROM upload_sessions WHERE session_id = $1`, sessionID)
	if err != nil {
		return SessionRecord{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return SessionRecord{}, ErrSessionNotFound
	}
	if err := rows.Scan(&rec.SessionID, &rec.TenantID, &rec.NodeID, &rec.Status,
		&rec.TotalSize, &rec.ExpectedChunks, &rec.ExpiresAt); err != nil {
		return SessionRecord{}, err
	}
	return rec, rows.Err()
}

func (s *PgStore) CommitFile(ctx context.Context, tenantID, sessionID uuid.UUID, contentSHA256 []byte, blocks []BlockMeta) (string, int, error) {
	tx, err := s.db.BeginWriteTx(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrDBUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. Lock session FOR UPDATE.
	var rec SessionRecord
	err = tx.QueryRow(ctx,
		`SELECT session_id::text, tenant_id::text, COALESCE(node_id::text,''),
		        status, total_size, expected_chunks, expires_at
		 FROM upload_sessions WHERE session_id = $1 FOR UPDATE`, sessionID,
	).Scan(&rec.SessionID, &rec.TenantID, &rec.NodeID, &rec.Status,
		&rec.TotalSize, &rec.ExpectedChunks, &rec.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, ErrSessionNotFound
		}
		return "", 0, err
	}

	// 2. Validate session state.
	if rec.Status == "COMPLETED" {
		return "", 0, ErrSessionCompleted
	}
	if time.Now().After(rec.ExpiresAt) {
		return "", 0, ErrSessionExpired
	}
	if rec.TenantID != tenantID.String() {
		return "", 0, ErrSessionTenantMismatch
	}
	if len(blocks) != rec.ExpectedChunks {
		return "", 0, fmt.Errorf("%w: expected %d blocks, got %d", ErrBadRequest, rec.ExpectedChunks, len(blocks))
	}

	// 3. Lock node FOR UPDATE.
	if rec.NodeID == "" {
		return "", 0, fmt.Errorf("%w: session has no node_id", ErrBadRequest)
	}
	nodeID, err := uuid.Parse(rec.NodeID)
	if err != nil {
		return "", 0, fmt.Errorf("invalid node_id in session: %w", err)
	}
	_, err = tx.Exec(ctx,
		`SELECT 1 FROM namespace_nodes WHERE node_id = $1 AND tenant_id = $2 FOR UPDATE`,
		nodeID, tenantID)
	if err != nil {
		return "", 0, fmt.Errorf("lock node: %w", err)
	}

	// 4. Next version number (fenced — node is locked above).
	var versionNumber int
	err = tx.QueryRow(ctx, `SELECT next_version_number($1)`, nodeID).Scan(&versionNumber)
	if err != nil {
		return "", 0, fmt.Errorf("next_version_number: %w", err)
	}

	// 5. Insert file version.
	var versionID uuid.UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO file_versions (node_id, version_number, total_size_bytes, content_sha256, created_by)
		 VALUES ($1, $2, $3, $4, $5) RETURNING version_id`,
		nodeID, versionNumber, rec.TotalSize, contentSHA256, tenantID,
	).Scan(&versionID)
	if err != nil {
		return "", 0, fmt.Errorf("insert file_version: %w", err)
	}

	// 6. Ensure CAS block rows exist (INSERT ON CONFLICT DO NOTHING).
	//    Ref-count is maintained exclusively by the manifest_block_added trigger.
	if len(blocks) > 0 {
		casArgs := make([]any, 0, len(blocks)*3)
		casPlaceholders := make([]string, 0, len(blocks))
		for i, b := range blocks {
			offset := i * 3
			casPlaceholders = append(casPlaceholders,
				fmt.Sprintf("($%d, $%d, $%d)", offset+1, offset+2, offset+3))
			casArgs = append(casArgs, MustDecodeHash(b.BlockHash), tenantID, b.SizeBytes)
		}
		casSQL := `INSERT INTO cas_blocks (block_hash, tenant_id, size_bytes)
		           VALUES ` + strings.Join(casPlaceholders, ", ") +
			` ON CONFLICT (block_hash) DO NOTHING`
		if _, err := tx.Exec(ctx, casSQL, casArgs...); err != nil {
			return "", 0, fmt.Errorf("upsert cas_blocks: %w", err)
		}
	}

	// 7. Insert manifest blocks (triggers bump ref_count).
	if len(blocks) > 0 {
		manArgs := make([]any, 0, len(blocks)*5)
		manPlaceholders := make([]string, 0, len(blocks))
		for i, b := range blocks {
			offset := i * 5
			manPlaceholders = append(manPlaceholders,
				fmt.Sprintf("($%d, $%d, $%d, $%d, $%d)", offset+1, offset+2, offset+3, offset+4, offset+5))
			manArgs = append(manArgs, versionID, MustDecodeHash(b.BlockHash), b.ChunkIndex, b.Offset, b.SizeBytes)
		}
		manSQL := `INSERT INTO file_manifest_blocks (version_id, block_hash, chunk_index, offset_bytes, size_bytes)
		           VALUES ` + strings.Join(manPlaceholders, ", ")
		if _, err := tx.Exec(ctx, manSQL, manArgs...); err != nil {
			return "", 0, fmt.Errorf("insert manifest: %w", err)
		}
	}

	// 8. Mark session completed.
	if _, err := tx.Exec(ctx,
		`UPDATE upload_sessions SET status = 'COMPLETED', chunks_received = $1
		 WHERE session_id = $2`, len(blocks), sessionID); err != nil {
		return "", 0, fmt.Errorf("complete session: %w", err)
	}

	// 9. Commit.
	if err := tx.Commit(ctx); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40001" {
			return "", 0, ErrSerialization
		}
		return "", 0, fmt.Errorf("commit tx: %w", err)
	}

	return versionID.String(), versionNumber, nil
}

func (s *PgStore) BumpCacheGeneration(ctx context.Context, tenantID uuid.UUID) error {
	if c := s.db.Cache(); c != nil {
		bctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := c.BumpGeneration(bctx, tenantID.String()); err != nil {
			s.logger.Error("cache generation bump failed after commit",
				"tenant_id", tenantID, "err", err)
			return err
		}
	}
	return nil
}

func (s *PgStore) ExpireStaleSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.ExecWithMetrics(ctx, database.OpWrite, "expire_sessions", "",
		`SELECT expire_stale_upload_sessions()`)
	if err != nil {
		return 0, err
	}
	return int64(tag.RowsAffected()), nil
}

func (s *PgStore) MarkBlockVerified(ctx context.Context, blockHash []byte) error {
	_, err := s.db.ExecWithMetrics(ctx, database.OpWrite, "mark_block_verified", "",
		`UPDATE cas_blocks SET verified = TRUE, updated_at = NOW() WHERE block_hash = $1`, blockHash)
	return err
}

// ---------------------------------------------------------------------------
// FakeStore — in-memory test double
// ---------------------------------------------------------------------------

type FakeStore struct {
	mu       sync.Mutex
	tenants  map[uuid.UUID]TenantQuota
	nodes    map[uuid.UUID]uuid.UUID
	sessions map[uuid.UUID]SessionRecord
	casSet   map[string]bool
	nextVer  int
	lastVer  string
	lastNum  int
	pingErr  error
}

var _ Store = (*FakeStore)(nil)

func NewFakeStore() *FakeStore {
	return &FakeStore{
		tenants:  make(map[uuid.UUID]TenantQuota),
		nodes:    make(map[uuid.UUID]uuid.UUID),
		sessions: make(map[uuid.UUID]SessionRecord),
		casSet:   make(map[string]bool),
	}
}

func (f *FakeStore) SeedTenant(id uuid.UUID, quota, used int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants[id] = TenantQuota{StorageQuotaBytes: quota, UsedBytes: used}
}

func (f *FakeStore) SeedCASBlock(hexHash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.casSet[hexHash] = true
}

func (f *FakeStore) SetPingErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingErr = err
}

func (f *FakeStore) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *FakeStore) GetTenantQuota(_ context.Context, tenantID uuid.UUID) (TenantQuota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, ok := f.tenants[tenantID]
	if !ok {
		return TenantQuota{}, ErrTenantNotFound
	}
	return q, nil
}

func (f *FakeStore) CreateFileNode(_ context.Context, tenantID uuid.UUID, _ *uuid.UUID, _ string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	nodeID := uuid.New()
	f.nodes[nodeID] = tenantID
	return nodeID, nil
}

func (f *FakeStore) ValidateFileNode(_ context.Context, tenantID, nodeID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	tid, ok := f.nodes[nodeID]
	if !ok {
		return ErrNodeNotFound
	}
	if tid != tenantID {
		return ErrNodeNotFound
	}
	return nil
}

func (f *FakeStore) BatchQueryExistingCAS(_ context.Context, hashes [][]byte) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		key := fmt.Sprintf("%x", h)
		result[key] = f.casSet[key]
	}
	return result, nil
}

func (f *FakeStore) CreateUploadSession(_ context.Context, tenantID, nodeID uuid.UUID, totalSize int64, chunks int, _, _ string) (uuid.UUID, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sid := uuid.New()
	f.sessions[sid] = SessionRecord{
		SessionID:      sid.String(),
		TenantID:       tenantID.String(),
		NodeID:         nodeID.String(),
		Status:         "INITIATED",
		TotalSize:      totalSize,
		ExpectedChunks: chunks,
		ExpiresAt:      time.Now().Add(15 * time.Minute),
	}
	return sid, time.Now().Add(15 * time.Minute), nil
}

func (f *FakeStore) GetSession(_ context.Context, sessionID uuid.UUID) (SessionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.sessions[sessionID]
	if !ok {
		return SessionRecord{}, ErrSessionNotFound
	}
	return rec, nil
}

func (f *FakeStore) CommitFile(_ context.Context, tenantID, sessionID uuid.UUID, _ []byte, blocks []BlockMeta) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.sessions[sessionID]
	if !ok {
		return "", 0, ErrSessionNotFound
	}
	if rec.Status == "COMPLETED" {
		return "", 0, ErrSessionCompleted
	}
	if time.Now().After(rec.ExpiresAt) {
		return "", 0, ErrSessionExpired
	}
	if rec.TenantID != tenantID.String() {
		return "", 0, ErrSessionTenantMismatch
	}
	if len(blocks) != rec.ExpectedChunks {
		return "", 0, fmt.Errorf("%w: expected %d blocks, got %d", ErrBadRequest, rec.ExpectedChunks, len(blocks))
	}
	f.nextVer++
	vid := uuid.New().String()
	f.lastVer = vid
	f.lastNum = f.nextVer
	for _, b := range blocks {
		f.casSet[b.BlockHash] = true
	}
	rec.Status = "COMPLETED"
	f.sessions[sessionID] = rec
	return vid, f.nextVer, nil
}

func (f *FakeStore) BumpCacheGeneration(_ context.Context, _ uuid.UUID) error { return nil }
func (f *FakeStore) ExpireStaleSessions(_ context.Context) (int64, error)    { return 0, nil }

func (f *FakeStore) MarkBlockVerified(_ context.Context, _ []byte) error { return nil }
