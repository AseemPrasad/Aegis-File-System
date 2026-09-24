package cdc

// DebeziumPayloadEnvelope represents the standard CDC JSON structure emitted by Debezium connectors.
type DebeziumPayloadEnvelope struct {
	Before    map[string]interface{} `json:"before"`
	After     map[string]interface{} `json:"after"`
	Source    DebeziumSource         `json:"source"`
	Op        string                 `json:"op"` // "c" = create/insert, "u" = update, "d" = delete
	Timestamp int64                  `json:"ts_ms"`
}

// DebeziumSource describes source database metadata in Debezium events.
type DebeziumSource struct {
	Version   string `json:"version"`
	Connector string `json:"connector"`
	Name      string `json:"name"`
	TSMS      int64  `json:"ts_ms"`
	DB        string `json:"db"`
	Schema    string `json:"schema"`
	Table     string `json:"table"`
}

// BlockCommittedEvent represents a typed CDC event parsed from file_manifest_blocks insertions.
type BlockCommittedEvent struct {
	VersionID string `json:"version_id"`
	BlockHash string `json:"block_hash"`
	Offset    int64  `json:"offset"`
	SizeBytes int64  `json:"size_bytes"`
	Timestamp string `json:"timestamp"`
}

// FileVersionCommittedEvent represents a typed CDC event parsed from file_versions insertions.
type FileVersionCommittedEvent struct {
	VersionID     string `json:"version_id"`
	NodeID        string `json:"node_id"`
	VersionNumber int    `json:"version_number"`
	TotalSizeBytes int64 `json:"total_size_bytes"`
	MimeType      string `json:"mime_type"`
	ContentSHA256 string `json:"content_sha256"`
	CreatedAt     string `json:"created_at"`
}
