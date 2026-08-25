package cas

import "github.com/prometheus/client_golang/prometheus"

// CASMetrics holds Prometheus collectors for CAS registry observability.
type CASMetrics struct {
	TotalBlocks       prometheus.Gauge
	TotalBytes        prometheus.Gauge
	OrphanBlocks      prometheus.Gauge
	OrphanBytes       prometheus.Gauge
	HotBlocks         prometheus.Gauge
	WarmBlocks        prometheus.Gauge
	ColdBlocks        prometheus.Gauge
	UnverifiedBlocks  prometheus.Gauge
	AvgRefCount       prometheus.Gauge
	MaxRefCount       prometheus.Gauge
	DeduplicationRatio prometheus.Gauge
	RefCountDistribution prometheus.Histogram
	GCSweepsTotal     prometheus.Counter
	GCBlocksDeleted   prometheus.Counter
	GCLastSweepDuration prometheus.Histogram
}

// NewCASMetrics registers all collectors with the given registerer.
func NewCASMetrics(reg prometheus.Registerer) *CASMetrics {
	m := &CASMetrics{
		TotalBlocks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "total_blocks", Help: "Total number of CAS block rows",
		}),
		TotalBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "total_bytes", Help: "Total bytes stored across all CAS blocks",
		}),
		OrphanBlocks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "orphan_blocks", Help: "Blocks with ref_count = 0 (GC candidates)",
		}),
		OrphanBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "orphan_bytes", Help: "Bytes in orphaned blocks",
		}),
		HotBlocks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "hot_blocks", Help: "Blocks in HOT tier (ref_count > 100)",
		}),
		WarmBlocks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "warm_blocks", Help: "Blocks in WARM tier (10 <= ref_count <= 100)",
		}),
		ColdBlocks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "cold_blocks", Help: "Blocks in COLD tier (ref_count < 10)",
		}),
		UnverifiedBlocks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "unverified_blocks", Help: "Blocks awaiting ETag verification",
		}),
		AvgRefCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "avg_ref_count", Help: "Average ref_count across all blocks",
		}),
		MaxRefCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "max_ref_count", Help: "Maximum ref_count across all blocks",
		}),
		DeduplicationRatio: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "dedup_ratio", Help: "Content size / stored size (higher = more dedup)",
		}),
		RefCountDistribution: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name:    "ref_count_distribution",
			Help:    "Distribution of ref_count values across blocks",
			Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000},
		}),
		GCSweepsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "gc_sweeps_total", Help: "Total number of GC sweep cycles",
		}),
		GCBlocksDeleted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name: "gc_blocks_deleted_total", Help: "Total blocks deleted by GC",
		}),
		GCLastSweepDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "aegis", Subsystem: "cas",
			Name:    "gc_sweep_duration_seconds",
			Help:    "Duration of last GC sweep",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60},
		}),
	}

	reg.MustRegister(
		m.TotalBlocks, m.TotalBytes,
		m.OrphanBlocks, m.OrphanBytes,
		m.HotBlocks, m.WarmBlocks, m.ColdBlocks,
		m.UnverifiedBlocks,
		m.AvgRefCount, m.MaxRefCount,
		m.DeduplicationRatio,
		m.RefCountDistribution,
		m.GCSweepsTotal, m.GCBlocksDeleted, m.GCLastSweepDuration,
	)
	return m
}

// RefreshFromStats updates all gauge metrics from a StorageStats snapshot.
// Should be called periodically (e.g. every 30s via a ticker).
func (m *CASMetrics) RefreshFromStats(stats *StorageStats, totalContentBytes int64) {
	if stats == nil || m == nil {
		return
	}
	m.TotalBlocks.Set(float64(stats.TotalBlocks))
	m.TotalBytes.Set(float64(stats.TotalBytes))
	m.OrphanBlocks.Set(float64(stats.OrphanBlocks))
	m.OrphanBytes.Set(float64(stats.OrphanBytes))
	m.HotBlocks.Set(float64(stats.HotBlocks))
	m.WarmBlocks.Set(float64(stats.WarmBlocks))
	m.ColdBlocks.Set(float64(stats.ColdBlocks))
	m.UnverifiedBlocks.Set(float64(stats.UnverifiedBlocks))
	m.AvgRefCount.Set(stats.AvgRefCount)
	m.MaxRefCount.Set(float64(stats.MaxRefCount))

	if stats.TotalBytes > 0 && totalContentBytes > 0 {
		m.DeduplicationRatio.Set(float64(totalContentBytes) / float64(stats.TotalBytes))
	}
}

// ObserveRefCounts records a ref_count distribution sample.
func (m *CASMetrics) ObserveRefCounts(refCounts []int64) {
	for _, rc := range refCounts {
		m.RefCountDistribution.Observe(float64(rc))
	}
}
