package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aegis-dev/aegis/internal/derivation"
	"github.com/aegis-dev/aegis/internal/events"
	"github.com/aegis-dev/aegis/internal/workers"
)

func main() {
	var (
		kafkaBrokers = flag.String("kafka-brokers", getEnv("KAFKA_BROKERS", "localhost:9092"), "Comma-separated Kafka brokers")
		groupID      = flag.String("group-id", getEnv("KAFKA_GROUP_ID", "aegis-derivation-workers"), "Consumer Group ID")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("starting aegis derivation worker microservice",
		"brokers", *kafkaBrokers,
		"group_id", *groupID)

	// Initialize derivation workers with fake scanner implementations
	scanner := workers.NewFakeScanner()
	extractor := workers.NewFakeTextExtractor()
	processor := workers.NewFakeVideoProcessor()
	blockReader := workers.NewFakeBlockReader()

	clamWorker := workers.NewClamAVWorker(scanner, blockReader)
	ocrWorker := workers.NewOCRWorker(extractor, blockReader)
	ffmpegWorker := workers.NewFFmpegWorker(processor, blockReader)

	workerList := []derivation.Worker{clamWorker, ocrWorker, ffmpegWorker}
	pool := derivation.NewWorkerPool(workerList, derivation.PoolConfig{
		Logger: logger,
	})

	brokers := strings.Split(*kafkaBrokers, ",")
	consumer, err := events.NewDerivationConsumer(events.ConsumerConfig{
		Brokers:  brokers,
		GroupID:  *groupID,
		Topic:    events.TopicDerivationTasks,
		DLQTopic: events.TopicDerivationDLQ,
		Logger:   logger,
	}, pool)

	if err != nil {
		logger.Error("failed to create kafka consumer", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := consumer.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("consumer loop terminated unexpectedly", "err", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down derivation worker microservice gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := consumer.Close(); err != nil {
		logger.Error("error closing consumer", "err", err)
	} else {
		logger.Info("derivation worker shutdown complete")
	}
	_ = shutdownCtx
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
