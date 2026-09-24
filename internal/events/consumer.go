package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aegis-dev/aegis/internal/derivation"
	"github.com/segmentio/kafka-go"
)

// ConsumerConfig configures the Kafka consumer group worker.
type ConsumerConfig struct {
	Brokers  []string
	GroupID  string
	Topic    string
	DLQTopic string
	Logger   *slog.Logger
}

// DerivationConsumer processes Kafka messages using Consumer Groups with manual offset commits.
type DerivationConsumer struct {
	reader *kafka.Reader
	dlq    *kafka.Writer
	pool   *derivation.WorkerPool
	logger *slog.Logger
}

// NewDerivationConsumer creates a Kafka consumer group processor.
func NewDerivationConsumer(cfg ConsumerConfig, pool *derivation.WorkerPool) (*DerivationConsumer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("brokers list cannot be empty")
	}
	if cfg.Topic == "" {
		cfg.Topic = TopicDerivationTasks
	}
	if cfg.DLQTopic == "" {
		cfg.DLQTopic = TopicDerivationDLQ
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.Brokers,
		GroupID:        cfg.GroupID,
		Topic:          cfg.Topic,
		MinBytes:       10,
		MaxBytes:       10 << 20, // 10MB
		CommitInterval: 0,       // Manual explicit offset commits
	})

	dlqWriter := &kafka.Writer{
		Addr:  kafka.TCP(cfg.Brokers...),
		Topic: cfg.DLQTopic,
	}

	return &DerivationConsumer{
		reader: reader,
		dlq:    dlqWriter,
		pool:   pool,
		logger: cfg.Logger,
	}, nil
}

// Start begins processing loop until context cancellation.
func (c *DerivationConsumer) Start(ctx context.Context) error {
	c.logger.Info("starting kafka derivation consumer group loop")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := c.reader.FetchMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				c.logger.Error("failed to fetch message from kafka", "err", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if err := c.processMessage(ctx, msg); err != nil {
				c.logger.Error("message processing failed; routing to DLQ", "key", string(msg.Key), "err", err)
				_ = c.sendToDLQ(ctx, msg, err)
			}

			// Manual Explicit Offset Commit after processing or DLQ routing
			if err := c.reader.CommitMessages(ctx, msg); err != nil {
				c.logger.Error("failed to commit message offset", "offset", msg.Offset, "err", err)
			}
		}
	}
}

func (c *DerivationConsumer) processMessage(ctx context.Context, msg kafka.Message) error {
	var task DerivationTaskEvent
	if err := json.Unmarshal(msg.Value, &task); err != nil {
		return fmt.Errorf("unmarshal error: %w", err)
	}

	event := derivation.Event{
		VersionID: task.VersionID,
		TenantID:  task.TenantID,
		MimeType:  task.MimeType,
	}

	results := c.pool.ProcessEvent(ctx, event)
	for _, res := range results {
		if res.Status == derivation.StatusFailed {
			return fmt.Errorf("worker %s failed: %s", res.WorkerName, res.Error)
		}
	}

	return nil
}

func (c *DerivationConsumer) sendToDLQ(ctx context.Context, msg kafka.Message, errReason error) error {
	dlqPayload := struct {
		OriginalPayload string `json:"original_payload"`
		ErrorReason     string `json:"error_reason"`
		FailedAt        string `json:"failed_at"`
	}{
		OriginalPayload: string(msg.Value),
		ErrorReason:     errReason.Error(),
		FailedAt:        time.Now().Format(time.RFC3339),
	}

	val, _ := json.Marshal(dlqPayload)
	return c.dlq.WriteMessages(ctx, kafka.Message{
		Key:   msg.Key,
		Value: val,
	})
}

// Close closes consumer reader and DLQ writer.
func (c *DerivationConsumer) Close() error {
	_ = c.dlq.Close()
	return c.reader.Close()
}
