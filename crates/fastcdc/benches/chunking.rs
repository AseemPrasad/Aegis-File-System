//! Baseline benchmarks establishing the performance floor before PROMPT 3.1
//! lands the real cut-point search. CI's perf-gate job stores this as the
//! initial criterion baseline so regressions are caught relative to history.

use aegis_fastcdc::{MAX_CHUNK, MIN_CHUNK};
use criterion::{criterion_group, criterion_main, Criterion};
use rand::RngCore;
use sha2::{Digest, Sha256};

fn bench_sha256_throughput_baseline(c: &mut Criterion) {
    // Hashing dominates per-chunk cost; this pins the cryptographic floor.
    // PROMPT 3.1 adds: gear-scan scalar vs simd, and end-to-end next_chunk().
    let mut buf = vec![0u8; MAX_CHUNK];
    rand::thread_rng().fill_bytes(&mut buf);

    let mut group = c.benchmark_group("baseline");
    group.throughput(criterion::Throughput::Bytes(buf.len() as u64));

    group.bench_function("sha256_4mib", |b| {
        b.iter(|| {
            let mut h = Sha256::new();
            h.update(&buf);
            h.finalize()
        })
    });

    group.bench_function("memset_scan_64k_window", |b| {
        b.iter(|| buf[..MIN_CHUNK].iter().fold(0u32, |acc, x| acc.wrapping_add(*x as u32)))
    });

    group.finish();
}

criterion_group!(benches, bench_sha256_throughput_baseline);
criterion_main!(benches);
