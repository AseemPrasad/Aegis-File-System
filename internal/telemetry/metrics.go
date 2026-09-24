package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// DeepMetrics holds extended observability collectors across CAS, S3, Kafka, and DB pools.
type DeepMetrics struct {
	DedupBytesSavedTotal prometheus.Counter
	S3IOLatency          *prometheus.HistogramVec
	KafkaConsumerLag     *prometheus.GaugeVec
	DBPoolWaitDuration   prometheus.Histogram
}

func NewDeepMetrics(reg prometheus.Registerer) *DeepMetrics {
	factory := promauto.With(reg)
	return &DeepMetrics{
		DedupBytesSavedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "aegis_dedup_bytes_saved_total",
			Help: "Total bytes saved via global CAS deduplication.",
		}),
		S3IOLatency: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_s3_io_latency_seconds",
			Help:    "S3 payload I/O transfer latency distribution.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
		}, []string{"operation"}),
		KafkaConsumerLag: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_kafka_consumer_lag",
			Help: "Kafka consumer group partition lag.",
		}, []string{"topic", "partition"}),
		DBPoolWaitDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "aegis_db_pool_wait_duration_seconds",
			Help:    "Database connection pool queue wait duration.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
		}),
	}
}
