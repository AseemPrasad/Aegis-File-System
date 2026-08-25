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

go 1.25.0

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

	// Structured logging — stdlib slog (go1.21+) chosen deliberately:
	// zero-dependency JSON handler, log/slog satisfies the structured-logging
	// contract in IC-3/PROMPT 10.1 without pulling zap/zerplog.
	// (stdlib — no require line)

	// errgroup for bounded concurrent chunk fan-out in tests/utilities.
	golang.org/x/sync v0.22.0
	google.golang.org/protobuf v1.36.10 // indirect
)

require (
	github.com/aws/aws-sdk-go-v2 v1.43.7
	github.com/aws/aws-sdk-go-v2/service/s3 v1.107.3
	github.com/minio/minio-go/v7 v7.3.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.18 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.38 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.37 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.38 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.38 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.38 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.39 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.38 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.39 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.33.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.38.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.45.7 // indirect
	github.com/aws/smithy-go v1.27.8 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/ini.v1 v1.67.3 // indirect
)
