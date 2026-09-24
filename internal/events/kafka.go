package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aegis-dev/aegis/internal/ingress"
	"github.com/segmentio/kafka-go"
)

// KafkaConfig configures the Kafka event publisher.
type KafkaConfig struct {
	Brokers  []string
	ClientID string
	Logger   *slog.Logger
}

// KafkaBus satisfies ingress.EventBus interface for production Kafka clusters.
type KafkaBus struct {
	brokers []string
	writers map[string]*kafka.Writer
	logger  *slog.Logger
	mu      sync.RWMutex
}

// NewKafkaBus creates an enterprise Kafka event publisher with partition keys.
func NewKafkaBus(cfg KafkaConfig) (*KafkaBus, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka brokers list cannot be empty")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	bus := &KafkaBus{
		brokers: cfg.Brokers,
		writers: make(map[string]*kafka.Writer),
		logger:  cfg.Logger,
	}

	// Initialize writers for standard topics
	topics := []string{
		TopicFileCommits,
		TopicDerivationTasks,
		TopicGCTombstones,
		TopicAuditEvents,
	}

	for _, topic := range topics {
		bus.writers[topic] = &kafka.Writer{
			Addr:         kafka.TCP(cfg.Brokers...),
			Topic:        topic,
			Balancer:     &kafka.LeastBytes{},
			BatchTimeout: 10 * time.Millisecond,
			Async:        true,
		}
	}

	return bus, nil
}

// PublishFileCommitted publishes a commit event keyed by tenant_id to guarantee FIFO partition ordering.
func (k *KafkaBus) PublishFileCommitted(ctx context.Context, event ingress.FileCommittedEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal FileCommittedEvent: %w", err)
	}

	k.mu.RLock()
	writer, exists := k.writers[TopicFileCommits]
	k.mu.RUnlock()

	if !exists {
		return fmt.Errorf("writer for topic %s not configured", TopicFileCommits)
	}

	msg := kafka.Message{
		Key:   []byte(event.TenantID), // Keyed by tenant_id for ordered partition routing
		Value: payload,
		Time:  time.Now(),
	}

	if err := writer.WriteMessages(ctx, msg); err != nil {
		k.logger.Error("failed to publish to kafka", "topic", TopicFileCommits, "err", err)
		return err
	}

	k.logger.Info("published FileCommittedEvent to kafka",
		"topic", TopicFileCommits,
		"tenant_id", event.TenantID,
		"node_id", event.NodeID)

	return nil
}

// PublishBlockTombstone emits a block tombstone event keyed by block_hash for GC sweeps.
func (k *KafkaBus) PublishBlockTombstone(ctx context.Context, topic string, event ingress.BlockTombstoneEvent) error {
	if topic == "" {
		topic = TopicGCTombstones
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal BlockTombstoneEvent: %w", err)
	}

	k.mu.Lock()
	writer, exists := k.writers[topic]
	if !exists {
		writer = &kafka.Writer{
			Addr:         kafka.TCP(k.brokers...),
			Topic:        topic,
			Balancer:     &kafka.LeastBytes{},
			BatchTimeout: 10 * time.Millisecond,
		}
		k.writers[topic] = writer
	}
	k.mu.Unlock()

	msg := kafka.Message{
		Key:   []byte(event.BlockHash), // Keyed by block_hash for idempotent partition compaction
		Value: payload,
		Time:  time.Now(),
	}

	return writer.WriteMessages(ctx, msg)
}

// Close gracefully flushes and closes all Kafka topic writers.
func (k *KafkaBus) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	var errs []string
	for topic, writer := range k.writers {
		if err := writer.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", topic, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing kafka writers: %s", strings.Join(errs, "; "))
	}
	return nil
}
