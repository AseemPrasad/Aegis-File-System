//! PROMPT 3.1 performance benchmarks.
//!
//! Run: `cargo bench` (all) or `cargo bench -- fastcdc_ --save-baseline p31`
//! Throughput gates (acceptance): cut-search ≥ 2 GB/s on AVX2 hardware;
//! scalar-vs-SIMD speedup > 2×. Payload size honors `AEGIS_BENCH_MB`
//! (default 256 MiB; set 1024 for the full 1 GiB run).

// Bench bodies intentionally end in block expressions consumed by criterion;
// criterion_group! generates undocumented glue functions.
#![allow(missing_docs)]

use aegis_fastcdc::scan::find_cut;
use aegis_fastcdc::{chunk_all, cuts_all, Config};
use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion, Throughput};
use rand::rngs::StdRng;
use rand::{RngCore, SeedableRng};
use sha2::{Digest, Sha256};

fn bench_mb() -> usize {
    std::env::var("AEGIS_BENCH_MB")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(256)
}

/// Deterministic pseudo-random payload (`StdRng`, fixed seed) so every bench
/// invocation measures identical work across runs and machines.
fn payload(mb: usize) -> Vec<u8> {
    let mut buf = vec![0u8; mb * 1024 * 1024];
    StdRng::seed_from_u64(0xAE61_5CDC).fill_bytes(&mut buf);
    buf
}

/// Insert `ins_mb` of fresh random bytes in the middle of a copy of `base`.
fn with_insertion(base: &[u8], ins_mb: usize, salt: u64) -> Vec<u8> {
    let mut rng = StdRng::seed_from_u64(salt);
    let at = base.len() / 2;
    let mut out = Vec::with_capacity(base.len() + ins_mb * 1024 * 1024);
    out.extend_from_slice(&base[..at]);
    let mut ins = vec![0u8; ins_mb * 1024 * 1024];
    rng.fill_bytes(&mut ins);
    out.extend_from_slice(&ins);
    out.extend_from_slice(&base[at..]);
    out
}

fn throughput_group(c: &mut Criterion) {
    let data = payload(bench_mb());
    let cfg = Config::prompt_default();
    let mut group = c.benchmark_group("fastcdc");
    group.throughput(Throughput::Bytes(data.len() as u64));

    // End-to-end pipeline: refill + two-phase SIMD cut + SHA-256 per chunk.
    group.bench_function("1gb_random_end_to_end", |b| {
        b.iter(|| {
            let descs = chunk_all(std::io::Cursor::new(&data), cfg)
                .expect("in-memory reader cannot fail");
            criterion::black_box(descs.len())
        });
    });

    // Cut search alone (no SHA-256, no copies): the honest measure of the
    // Gear/SIMD core against the ≥2 GB/s acceptance gate.
    group.bench_function("cut_search_only", |b| {
        b.iter(|| criterion::black_box(scan_windows(&data, &cfg)));
    });
    group.finish();
}

fn scan_windows(data: &[u8], cfg: &Config) -> usize {
    let mut pos = 0usize;
    let mut chunks = 0usize;
    while pos < data.len() {
        let cap = (pos + cfg.max_chunk).min(data.len());
        if cap - pos <= cfg.min_chunk {
            pos = cap;
        } else {
            let p1_end = cap.min(pos + cfg.avg_chunk);
            let hit = find_cut(cfg.gear, &data[pos + cfg.min_chunk..p1_end], cfg.mask_s)
                .map(|l| pos + cfg.min_chunk + l)
                .or_else(|| {
                    (cap > pos + cfg.avg_chunk).then(|| {
                        find_cut(cfg.gear, &data[pos + cfg.avg_chunk..cap], cfg.mask_l)
                            .map_or(cap, |l| pos + cfg.avg_chunk + l)
                    })
                })
                .unwrap_or(cap);
            pos = hit;
        }
        chunks += 1;
    }
    chunks
}

fn simd_vs_scalar_group(c: &mut Criterion) {
    let data = payload(64.min(bench_mb()));
    let cfg = Config::prompt_default();
    let mut group = c.benchmark_group("simd_speedup");
    group.throughput(Throughput::Bytes(data.len() as u64));

    // Raw cut-search A/B over pre-sliced windows: no reader copies, no SHA.
    // This isolates exactly what the SIMD path accelerates. The label must
    // honor the env override or both runs would be mislabeled "avx2".
    let forced_scalar = std::env::var("AEGIS_FASTCDC_FORCE_SCALAR")
        .is_ok_and(|v| v.trim() == "1");
    let label = if forced_scalar {
        "scalar"
    } else {
        aegis_fastcdc::active_simd_path()
    };
    group.bench_function(BenchmarkId::new("scan", label), |b| {
        b.iter(|| criterion::black_box(scan_windows(&data, &cfg)));
    });

    // Stream-level reference point: shows how much buffering copies add on
    // top of the raw scan (historically ~3x on this host — see docs).
    group.bench_function("stream_cuts_all", |b| {
        b.iter(|| {
            let pts = cuts_all(std::io::Cursor::new(&data), cfg).expect("in-memory reader");
            criterion::black_box(pts.len())
        });
    });

    group.finish();
}

fn modifications_group(c: &mut Criterion) {
    let base = payload(32);
    let modified = with_insertion(&base, 2, 42);
    let cfg = Config::prompt_default();

    let mut group = c.benchmark_group("fastcdc_with_modifications");
    group.throughput(Throughput::Bytes(modified.len() as u64));
    group.bench_function("rechunk_after_10pct_insert", |b| {
        b.iter(|| {
            let d = chunk_all(std::io::Cursor::new(&modified), cfg)
                .expect("in-memory reader cannot fail");
            criterion::black_box(d.len());
        });
    });
    group.finish();
}

/// Fixed-size vs content-defined deduplication, measured as *rehashed bytes*
/// under an insertion — the economic metric CAS systems care about. Ratio
/// assertions live in tests/oracle.rs; this bench times the comparison.
fn dedup_vs_fixed_group(c: &mut Criterion) {
    let base = payload(32);
    let modified = with_insertion(&base, 2, 43);
    let cfg = Config::prompt_default();
    let mut group = c.benchmark_group("dedup_comparison");

    group.bench_function("fixed_1mib_hashes", |b| {
        b.iter(|| {
            let mut hashes = Vec::new();
            for block in modified.chunks(1024 * 1024) {
                hashes.push(Sha256::digest(block));
            }
            hashes.len()
        });
    });

    group.bench_function("fastcdc_hashes", |b| {
        b.iter(|| {
            chunk_all(std::io::Cursor::new(&modified), cfg)
                .map_or(0, |d| criterion::black_box(d.len()))
        });
    });

    group.finish();
}

criterion_group!(
    benches,
    throughput_group,
    simd_vs_scalar_group,
    modifications_group,
    dedup_vs_fixed_group,
);
criterion_main!(benches);
