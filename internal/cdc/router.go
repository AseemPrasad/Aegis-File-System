package cdc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aegis-dev/aegis/internal/events"
	"github.com/segmentio/kafka-go"
)

// RouterConfig configures the CDC Event Router.
type RouterConfig struct {
	Brokers     []string
	GroupID     string
	InputTopic  string
	TargetTopic string
	Logger      *slog.Logger
}

// Router intercepts raw Debezium WAL events, strips database metadata, and routes typed tasks to Kafka topics.
type Router struct {
	reader *kafka.Reader
	writer *kafka.Writer
	logger *slog.Logger
}

// NewRouter creates a new CDC WAL event router instance.
func NewRouter(cfg RouterConfig) (*Router, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("brokers list cannot be empty")
	}
	if cfg.InputTopic == "" {
		cfg.InputTopic = "aegis-db.public.file_versions"
	}
	if cfg.TargetTopic == "" {
		cfg.TargetTopic = events.TopicDerivationTasks
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  cfg.Brokers,
		GroupID:  cfg.GroupID,
		Topic:    cfg.InputTopic,
		MinBytes: 10,
		MaxBytes: 10 << 20,
	})

	writer := &kafka.Writer{
		Addr:     kafka.TCP(cfg.Brokers...),
		Topic:    cfg.TargetTopic,
		Balancer: &kafka.LeastBytes{},
	}

	return &Router{
		reader: reader,
		writer: writer,
		logger: cfg.Logger,
	}, nil
}

// Start launches the CDC processing loop.
func (r *Router) Start(ctx context.Context) error {
	r.logger.Info("starting debezium cdc event router loop")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := r.reader.FetchMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				r.logger.Error("failed to fetch cdc message", "err", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if err := r.routeMessage(ctx, msg); err != nil {
				r.logger.Error("failed to route cdc message", "err", err)
			}

			_ = r.reader.CommitMessages(ctx, msg)
		}
	}
}

func (r *Router) routeMessage(ctx context.Context, msg kafka.Message) error {
	var env DebeziumPayloadEnvelope
	if err := json.Unmarshal(msg.Value, &env); err != nil {
		return fmt.Errorf("unmarshal debezium envelope: %w", err)
	}

	// Only process INSERT operations ("c" = Create)
	if env.Op != "c" || env.After == nil {
		return nil
	}

	versionID, _ := env.After["version_id"].(string)
	nodeID, _ := env.After["node_id"].(string)
	mimeType, _ := env.After["mime_type"].(string)
	contentSHA, _ := env.After["content_sha256"].(string)

	// Extract tenant_id from WAL envelope (created_by or tenant_id) or fallback safely
	tenantID, _ := env.After["tenant_id"].(string)
	if tenantID == "" {
		tenantID, _ = env.After["created_by"].(string)
	}
	if tenantID == "" {
		tenantID = "00000000-0000-0000-0000-000000000001"
	}

	task := events.DerivationTaskEvent{
		EventID:   fmt.Sprintf("cdc-%d", env.Timestamp),
		TenantID:  tenantID,
		VersionID: versionID,
		NodeID:    nodeID,
		BlockHash: contentSHA,
		MimeType:  mimeType,
		Timestamp: time.UnixMilli(env.Timestamp).Format(time.RFC3339),
	}

	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	return r.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(versionID),
		Value: payload,
	})
}

// Close closes reader and writer.
func (r *Router) Close() error {
	_ = r.writer.Close()
	return r.reader.Close()
}
