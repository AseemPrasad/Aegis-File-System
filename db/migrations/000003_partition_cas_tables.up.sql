-- Up migration: 000003_partition_cas_tables
-- 1. Declarative 16-way Hash Partitioning on block_hash for cas_blocks
CREATE TABLE cas_blocks_partitioned (
    block_hash VARCHAR(64) NOT NULL,
    size_bytes INT NOT NULL,
    ref_count BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (block_hash)
) PARTITION BY HASH (block_hash);

CREATE TABLE cas_blocks_p0 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 0);
CREATE TABLE cas_blocks_p1 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 1);
CREATE TABLE cas_blocks_p2 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 2);
CREATE TABLE cas_blocks_p3 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 3);
CREATE TABLE cas_blocks_p4 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 4);
CREATE TABLE cas_blocks_p5 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 5);
CREATE TABLE cas_blocks_p6 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 6);
CREATE TABLE cas_blocks_p7 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 7);
CREATE TABLE cas_blocks_p8 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 8);
CREATE TABLE cas_blocks_p9 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 9);
CREATE TABLE cas_blocks_p10 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 10);
CREATE TABLE cas_blocks_p11 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 11);
CREATE TABLE cas_blocks_p12 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 12);
CREATE TABLE cas_blocks_p13 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 13);
CREATE TABLE cas_blocks_p14 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 14);
CREATE TABLE cas_blocks_p15 PARTITION OF cas_blocks_partitioned FOR VALUES WITH (MODULUS 16, REMAINDER 15);

-- 2. Declarative Monthly Range Partitioning for audit_logs
CREATE TABLE audit_logs_partitioned (
    log_id UUID DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    action VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (log_id, created_at)
) PARTITION BY RANGE (created_at);

CREATE TABLE audit_logs_2026_09 PARTITION OF audit_logs_partitioned
    FOR VALUES FROM ('2026-09-01 00:00:00+00') TO ('2026-10-01 00:00:00+00');
