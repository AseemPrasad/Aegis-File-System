package cdc_test

import (
	"encoding/json"
	"testing"

	"github.com/aegis-dev/aegis/internal/cdc"
)

func TestDebeziumEnvelopeUnmarshaling(t *testing.T) {
	rawJSON := `{
		"before": null,
		"after": {
			"version_id": "v-100",
			"node_id": "n-200",
			"mime_type": "application/pdf",
			"content_sha256": "abc123hash"
		},
		"source": {
			"db": "aegis",
			"table": "file_versions"
		},
		"op": "c",
		"ts_ms": 1700000000000
	}`

	var env cdc.DebeziumPayloadEnvelope
	if err := json.Unmarshal([]byte(rawJSON), &env); err != nil {
		t.Fatalf("failed to unmarshal debezium envelope: %v", err)
	}

	if env.Op != "c" {
		t.Errorf("op = %s, want c", env.Op)
	}
	if env.After["version_id"] != "v-100" {
		t.Errorf("version_id = %v, want v-100", env.After["version_id"])
	}
}
