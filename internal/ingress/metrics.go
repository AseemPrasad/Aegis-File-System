package ingress

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// IngestMetrics holds Prometheus collectors for the ingestion engine.
type IngestMetrics struct {
	InitiateTotal   *prometheus.CounterVec
	CommitTotal     *prometheus.CounterVec
	InitiateLatency *prometheus.HistogramVec
	CommitLatency   *prometheus.HistogramVec
	CASHitsTotal    prometheus.Counter
	UploadsTotal    prometheus.Counter
	QuotaExceeded   prometheus.Counter
}

func NewIngestMetrics(reg prometheus.Registerer) *IngestMetrics {
	factory := promauto.With(reg)
	return &IngestMetrics{
		InitiateTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_ingest_initiate_total",
			Help: "Total HandleInitiate calls by status.",
		}, []string{"status"}),
		CommitTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_ingest_commit_total",
			Help: "Total HandleCommit calls by status.",
		}, []string{"status"}),
		InitiateLatency: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_ingest_initiate_duration_seconds",
			Help:    "HandleInitiate latency.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		}, []string{"status"}),
		CommitLatency: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_ingest_commit_duration_seconds",
			Help:    "HandleCommit latency.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		}, []string{"status"}),
		CASHitsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "aegis_ingest_cas_hits_total",
			Help: "Blocks that were already in CAS (dedup savings).",
		}),
		UploadsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "aegis_ingest_uploads_total",
			Help: "Blocks that required upload (not in CAS).",
		}),
		QuotaExceeded: factory.NewCounter(prometheus.CounterOpts{
			Name: "aegis_ingest_quota_exceeded_total",
			Help: "Initiate requests rejected for quota.",
		}),
	}
}
