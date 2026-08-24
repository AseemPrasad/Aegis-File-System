// ============================================================================
// Project Aegis — Go Orchestration Engine module
// PROMPT 1.2 (Development Environment & Dependency Selection)
//
// NOTE: replace the module path with the canonical VCS path when the repo
// remote is finalized; all internal import prefixes follow it.
//
// PINNING POLICY (docs/DEPENDENCIES.md §Pinning):
//   * Direct deps pinned to exact patch versions here.
//   * `go.sum` is committed once service code imports these modules
//     (`make tidy` regenerates); govulncheck gates in CI weekly.
//   * Test-only modules live in the same file (Go has no dev-deps split);
//     they are tagged with comments and never imported by prod code.
//
// Indirect requirements are intentionally omitted — `go mod tidy` resolves
// them after PROMPT 2.2+ lands real imports against these packages.
// ============================================================================

module github.com/aegis-dev/aegis

go 1.23

require (
	// UUID generation (tenant_id, node_id, session_id, version_id).
	// google/uuid: smallest, most audited implementation; RFC 4122 compliant.
	// We standardize on v4 (random) for IDs, matching schema defaults
	// gen_random_uuid(); no crypto dependency conflicts (uses crypto/rand).
	github.com/google/uuid v1.6.0
	// PostgreSQL driver + pgxpool pooling.
	// Why pgx over database/sql + lib/pq:
	//   lib/pq is in maintenance mode; pgx speaks the binary protocol natively
	//   (~2-3x fewer allocations per query), supports COPY, LISTEN/NOTIFY,
	//   prepared-statement caching, and pgxpool gives us the read/write pool
	//   split demanded by contract IC-3 with per-pool metrics hooks.
	// Memory/GC: statement descriptions cached → no per-exec parse round trip;
	//   binary encoding avoids []byte→string churn that inflates GC pressure
	//   under the 1000-concurrent-client load target (SLA row 2).
	github.com/jackc/pgx/v5 v5.7.1

	// HTTP routing for the stateless ingress engine.
	// Why echo over gin:
	//   echo's router is a radix tree with zero-allocation route matching;
	//   gin's binding relies more heavily on reflection (more allocs → more
	//   GC). echo bundles rate-limiter, timeout, recover, request-id
	//   middleware natively — we need exactly those at the edge boundary of
	//   IC-1/IC-2. Both are fine; echo wins the memory/GC criterion.
	github.com/labstack/echo/v4 v4.12.0

	// Prometheus metrics (contract IC-3 observability; SLA verification).
	github.com/prometheus/client_golang v1.20.0

	// Redis client (Bloom pre-filter, nonces, sessions, fencing tokens).
	// Why go-redis/v9 over redigo / rueidis:
	//   redigo is effectively unmaintained; rueidis (RESP3, client-side
	//   caching) is technically superior but younger — revisit at PROMPT 5.1.
	//   go-redis v9 has cluster/sentinel parity with production ElastiCache,
	//   pipelining, and hook-based metrics injection for Prometheus.
	// Memory/GC: connection pool reuses buffers; SETNX nonce path is O(1)
	//   allocs on the hot replay-check path (invariant I-3 EP-4).
	github.com/redis/go-redis/v9 v9.6.1

	// ---- TEST-ONLY (never imported by production packages) ----
	// Assertions for integration suites (PROMPT 9.1).
	github.com/stretchr/testify v1.9.0
	// Real-container integration tests (PostgreSQL/MinIO/Redis) per CI gate.
	github.com/testcontainers/testcontainers-go v0.33.0

	// Kafka/Redpanda client.
	// Why franz-go over sarama / segmentio kafka-go / confluent-go:
	//   sarama: heavy allocations historically + slower maintenance cadence;
	//   confluent client requires librdkafka via cgo (cross-compile pain,
	//   CGO_ENABLED=0 distroless images impossible);
	//   franz-go: pure Go, highest-throughput pure-Go producer, KIP feature
	//   parity (idempotent producer, transactions), group-balancing built-in.
	//   CDC emission in HandleCommit (step C10) is produce-only on the hot
	//   path → batched, compressed (zstd) records keep p99 tail flat.
	github.com/twmb/franz-go v1.18.0

	// Structured logging — stdlib slog (go1.21+) chosen deliberately:
	// zero-dependency JSON handler, log/slog satisfies the structured-logging
	// contract in IC-3/PROMPT 10.1 without pulling zap/zerplog.
	// (stdlib — no require line)

	// errgroup for bounded concurrent chunk fan-out in tests/utilities.
	golang.org/x/sync v0.8.0

	// gRPC (inter-service control channel; worker protocol in PROMPT 7.1).
	google.golang.org/grpc v1.66.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	golang.org/x/crypto v0.27.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
)
