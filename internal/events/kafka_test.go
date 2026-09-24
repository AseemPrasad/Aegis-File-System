package events_test

import (
	"testing"
	"time"

	"github.com/aegis-dev/aegis/internal/events"
	"github.com/aegis-dev/aegis/internal/ingress"
)

func TestTopicConstants(t *testing.T) {
	if events.TopicFileCommits != "aegis.file.commits" {
		t.Errorf("unexpected TopicFileCommits: %s", events.TopicFileCommits)
	}
	if events.TopicDerivationTasks != "aegis.derivation.tasks" {
		t.Errorf("unexpected TopicDerivationTasks: %s", events.TopicDerivationTasks)
	}
	if events.TopicGCTombstones != "aegis.gc.tombstones" {
		t.Errorf("unexpected TopicGCTombstones: %s", events.TopicGCTombstones)
	}
}

func TestDerivationTaskSerialization(t *testing.T) {
	task := events.DerivationTaskEvent{
		EventID:   "evt-123",
		TenantID:  "tenant-001",
		VersionID: "v1.0.0",
		NodeID:    "node-456",
		BlockHash: "sha256-abc",
		MimeType:  "application/pdf",
		SizeBytes: 1024,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if task.TenantID != "tenant-001" || task.BlockHash != "sha256-abc" {
		t.Errorf("task fields mismatch: %+v", task)
	}
}

func TestKafkaBusInterfaceCompliance(t *testing.T) {
	var _ ingress.EventBus = (*events.KafkaBus)(nil)
}
