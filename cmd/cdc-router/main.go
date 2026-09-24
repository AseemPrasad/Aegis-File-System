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

	"github.com/aegis-dev/aegis/internal/cdc"
	"github.com/aegis-dev/aegis/internal/events"
)

func main() {
	var (
		kafkaBrokers = flag.String("kafka-brokers", getEnv("KAFKA_BROKERS", "localhost:9092"), "Comma-separated Kafka brokers")
		inputTopic   = flag.String("input-topic", getEnv("CDC_INPUT_TOPIC", "aegis-db.public.file_versions"), "Debezium CDC input topic")
		groupID      = flag.String("group-id", getEnv("CDC_GROUP_ID", "aegis-cdc-router-group"), "Consumer Group ID")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("starting aegis debezium cdc router microservice",
		"brokers", *kafkaBrokers,
		"input_topic", *inputTopic)

	brokers := strings.Split(*kafkaBrokers, ",")
	router, err := cdc.NewRouter(cdc.RouterConfig{
		Brokers:     brokers,
		GroupID:     *groupID,
		InputTopic:  *inputTopic,
		TargetTopic: events.TopicDerivationTasks,
		Logger:      logger,
	})

	if err != nil {
		logger.Error("failed to create cdc router", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := router.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("cdc router loop terminated unexpectedly", "err", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down debezium cdc router microservice gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := router.Close(); err != nil {
		logger.Error("error closing cdc router", "err", err)
	} else {
		logger.Info("cdc router shutdown complete")
	}
	_ = shutdownCtx
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
