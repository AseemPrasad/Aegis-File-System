//! PROMPT 3.1 verification battery: SIMD/scalar equivalence, two-phase
//! masking oracle checks, edge cases, content-awareness (modification
//! locality), determinism, size distribution, and reader robustness.

// Test-only numeric plumbing: xorshift byte extraction and ratio math are
// deliberately lossy-by-design; precision claims live in the library.
#![allow(
    clippy::cast_possible_truncation,
    clippy::cast_precision_loss,
    clippy::cast_sign_loss
)]

use std::collections::HashSet;
use std::io::{self, Read};

use aegis_fastcdc::{
    chunk_all, entropy_q8, find_cut_scalar, find_cut_simd_forced, Config,
    CutPhase, FastCDCStream, AVG_CHUNK, MASK_S, MAX_CHUNK, MIN_CHUNK,
};
use rand::rngs::StdRng;
use rand::{RngCore, SeedableRng};
use sha2::{Digest, Sha256};

fn cfg() -> Config {
    Config {
        compute_features: true,
        ..Config::prompt_default()
    }
}

fn xorshift_payload(len: usize, seed: u64) -> Vec<u8> {
    let mut s = seed | 1;
    let mut out = vec![0u8; len];
    for b in &mut out {
        s ^= s << 13;
        s ^= s >> 7;
        s ^= s << 17;
        *b = (s >> 24) as u8;
    }
    out
}

// ---------------------------------------------------------------------------
// 1. Fingerprint formula + SIMD/scalar equivalence
// ---------------------------------------------------------------------------

/// The spec formula, evaluated byte-by-byte — the normative reference.
fn reference_fingerprint(gear: &[u32; 256], data: &[u8]) -> u32 {
    let mut fp: u32 = 0;
    for &b in data {
        fp = (fp << 1).wrapping_add(gear[usize::from(b)]);
    }
    fp
}

#[test]
fn fingerprint_formula_matches_spec() {
    let gear: [u32; 256] = aegis_fastcdc::generate_matrix(0x00C0_FFEE);
    let data = xorshift_payload(64, 9);

    // Prefix-31 state seeded into the scan must equal hand-stepping.
    let prefix_fp = reference_fingerprint(&gear, &data[..31]);
    assert_eq!(prefix_fp, {
        // Independent literal transcription of the spec line:
        let mut f: u32 = 0;
        for i in 0..31 {
            f = (f << 1).wrapping_add(gear[usize::from(data[i])]);
        }
        f
    });

    // Seeded continuation finds the same first cut as manual stepping.
    let rest = &data[31..];
    let mut manual = prefix_fp;
    let expected = rest
        .iter()
        .position(|&rb| {
            manual = (manual << 1).wrapping_add(gear[usize::from(rb)]);
            manual & MASK_S == 0
        })
        .map(|p| p + 1);
    assert_eq!(
        aegis_fastcdc::find_cut_scalar_from(&gear, rest, MASK_S, prefix_fp),
        expected
    );
}

#[test]
fn scalar_simd_equivalence() {
    if aegis_fastcdc::active_simd_path() == "scalar" {
        eprintln!("skipping: no SIMD path on this host");
        return;
    }
    let gear = Config::prompt_default().gear;
    let mut rng = StdRng::seed_from_u64(0xBEEF);
    // Sizes straddle group widths (4/8) and phase boundaries.
    let sizes = [
        3, 7, 8, 9, 15, 16, 17, 255, 4096, MIN_CHUNK - 1, MIN_CHUNK, MIN_CHUNK + 1,
        AVG_CHUNK - 7, AVG_CHUNK, AVG_CHUNK + 123, MAX_CHUNK / 2,
    ];
    for size in sizes {
        let mut data = vec![0u8; size];
        rng.fill_bytes(&mut data);
        for mask in [MASK_S, 0x0007_FFFF, 0x0000_FFFF, 1] {
            assert_eq!(
                find_cut_simd_forced(gear, &data, mask),
                find_cut_scalar(gear, &data, mask),
                "divergence at size={size} mask={mask:#x}"
            );
        }
    }
}

// ---------------------------------------------------------------------------
// 2. Stream oracle equivalence (two-phase driver vs naive simulation)
// ---------------------------------------------------------------------------

/// Naive, obviously-correct simulation: one running fingerprint per chunk,
/// checked against `MASK_S` in `[min, avg)` then `MASK_L` in `[avg, max]`.
fn naive_chunk_boundaries(data: &[u8], config: &Config) -> Vec<usize> {
    let mut bounds = Vec::new();
    let mut start = 0usize;
    while start < data.len() {
        let cap = (start + config.max_chunk).min(data.len());
        if cap - start <= config.min_chunk {
            // Short window at EOF (or hard-max ceiling): whole remainder.
            bounds.push(cap - start);
            start = cap;
            continue;
        }
        // Warm-up consumes bytes before the minimum without checking.
        let mut fp: u32 = 0;
        for &b in &data[start..start + config.min_chunk] {
            fp = (fp << 1).wrapping_add(config.gear[usize::from(b)]);
        }
        let p1_end = cap.min(start + config.avg_chunk);
        let mut hit = None;
        for (pos, &b) in data.iter().enumerate().take(p1_end).skip(start + config.min_chunk) {
            fp = (fp << 1).wrapping_add(config.gear[usize::from(b)]);
            if fp & config.mask_s == 0 {
                hit = Some(pos + 1);
                break;
            }
        }
        if hit.is_none() && cap > start + config.avg_chunk {
            for (pos, &b) in
                data.iter().enumerate().take(cap).skip(start + config.avg_chunk)
            {
                fp = (fp << 1).wrapping_add(config.gear[usize::from(b)]);
                if fp & config.mask_l == 0 {
                    hit = Some(pos + 1);
                    break;
                }
            }
        }
        let cut = hit.unwrap_or(cap);
        bounds.push(cut - start);
        start = cut;
    }
    bounds
}

#[test]
fn stream_matches_naive_oracle() {
    let config = cfg();
    let mut rng = StdRng::seed_from_u64(0xD00D);
    let sizes = [
        0, 1, 100, MIN_CHUNK - 1, MIN_CHUNK, MIN_CHUNK + 5, 300 * 1024, AVG_CHUNK - 11,
        AVG_CHUNK, AVG_CHUNK + 777, MAX_CHUNK - 3, MAX_CHUNK, MAX_CHUNK + 42,
    ];
    for &size in &sizes {
        let mut data = vec![0u8; size];
        rng.fill_bytes(&mut data);
        let descs = chunk_all(io::Cursor::new(&data), config).expect("cursor cannot fail");
        let got: Vec<(u64, usize)> = descs.iter().map(|d| (d.offset, d.length)).collect();
        let expect = naive_chunk_boundaries(&data, &config);
        assert_eq!(
            got.len(),
            expect.len(),
            "chunk count mismatch at input size {size}"
        );
        let mut off = 0u64;
        for ((g_off, g_len), e_len) in got.iter().zip(expect.iter()) {
            assert_eq!(*g_off, off);
            assert_eq!(g_len, e_len, "size mismatch at offset {off}, input {size}");
            off += u64::try_from(*e_len).expect("len fits");
        }
        assert_eq!(
            usize::try_from(off).expect("offset fits usize"),
            size,
            "coverage mismatch at input size {size}"
        );
    }
}

// ---------------------------------------------------------------------------
// 3. Edge cases
// ---------------------------------------------------------------------------

#[test]
fn empty_file_yields_none() {
    let mut s = FastCDCStream::new(io::Cursor::new(Vec::<u8>::new())).expect("cfg ok");
    assert_eq!(s.next_chunk().expect("io"), None);
}

#[test]
fn file_smaller_than_min_is_single_chunk() {
    for size in [1, 1024, MIN_CHUNK - 1] {
        let data = xorshift_payload(size, 7);
        let descs = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
        assert_eq!(descs.len(), 1, "size {size}");
        assert_eq!(descs[0].length, size);
        assert_eq!(descs[0].offset, 0);
        assert_ne!(
            descs[0].content_features & CutPhase::EofTail.feature_bit(),
            0,
            "tail flag set"
        );
    }
}

#[test]
fn exact_min_file_is_single_chunk() {
    let data = xorshift_payload(MIN_CHUNK, 8);
    let descs = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
    assert_eq!(descs.len(), 1);
    assert_eq!(descs[0].length, MIN_CHUNK);
}

#[test]
fn repetitive_data_terminates_with_sane_chunks() {
    for fill in [0u8, 0xFF, 0x41] {
        let data = vec![fill; MAX_CHUNK + MIN_CHUNK];
        let descs = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
        assert!(!descs.is_empty());
        let total: usize = descs.iter().map(|d| d.length).sum();
        assert_eq!(total, data.len(), "coverage mismatch");
        for (i, d) in descs.iter().enumerate() {
            if i + 1 < descs.len() {
                assert!(d.length > MIN_CHUNK && d.length <= MAX_CHUNK, "run chunk {i} len {}", d.length);
            } else {
                assert!(d.length <= MAX_CHUNK, "tail len {}", d.length);
            }
        }
        // Constant bytes → zero entropy feature.
        assert_eq!(entropy_q8(&data[..1024]), 0);
        assert_eq!(descs[0].content_features & 0xFF, 0);
    }
}

#[test]
fn descriptors_reconstruct_sha_addresses() {
    let data = xorshift_payload(2 * MAX_CHUNK, 0xAB);
    let descs = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
    for d in &descs {
        let off = usize::try_from(d.offset).expect("offset fits usize");
        let digest = Sha256::digest(&data[off..off + d.length]);
        assert_eq!(digest[..], d.hash[..], "hash at offset {}", d.offset);
    }
    let total: usize = descs.iter().map(|d| d.length).sum();
    assert_eq!(total, data.len());
    let mut expect_off = 0u64;
    for d in &descs {
        assert_eq!(d.offset, expect_off);
        expect_off += d.length as u64;
    }
}

// ---------------------------------------------------------------------------
// 4. Determinism across clients
// ---------------------------------------------------------------------------

#[test]
fn identical_inputs_identical_chunks() {
    let data = xorshift_payload(5 * MIN_CHUNK, 0x5151);
    let a = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
    let b = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
    assert_eq!(a, b);
    // Different read granularity must produce identical descriptors:
    let c =
        chunk_all(SlowReader::new(data.clone(), 997), cfg()).expect("io");
    assert_eq!(a, c, "buffering granularity must not affect output");
}

/// Reader that hands out tiny, odd-sized chunks — simulates hostile sockets.
struct SlowReader {
    data: Vec<u8>,
    pos: usize,
    step: usize,
}

fn jitter(rng: &mut StdRng, span: u64) -> usize {
    let v = u64::from(u32::try_from(span).expect("span fits u32"));
    usize::try_from(rng.next_u64() % v).expect("jitter fits")
}
impl SlowReader {
    fn new(data: Vec<u8>, step: usize) -> Self {
        Self { data, pos: 0, step }
    }
}
impl Read for SlowReader {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if self.pos >= self.data.len() {
            return Ok(0);
        }
        let n = buf.len().min(self.step).min(self.data.len() - self.pos);
        buf[..n].copy_from_slice(&self.data[self.pos..self.pos + n]);
        self.pos += n;
        Ok(n)
    }
}

// ---------------------------------------------------------------------------
// 5. Content-awareness: modification locality vs fixed-size
// ---------------------------------------------------------------------------

/// Fraction of BYTES in `modified` covered by chunks whose hash does NOT
/// appear in the base file's chunk set (i.e., content that must be stored /
/// hashed anew). This is the deduplication-relevant cost metric.
fn rehashed_fraction(base: &[u8], modified: &[u8], content_defined: bool) -> f64 {
    const FIXED_WINDOW: usize = 1024 * 1024;
    fn hashes_of(data: &[u8], content_defined: bool) -> HashSet<[u8; 32]> {
        if content_defined {
            chunk_all(io::Cursor::new(data), Config::prompt_default())
                .expect("io")
                .into_iter()
                .map(|d| d.hash)
                .collect()
        } else {
            data.chunks(FIXED_WINDOW)
                .map(|b| Sha256::digest(b).into())
                .collect()
        }
    }

    let mod_descs = chunk_all(io::Cursor::new(modified), Config::prompt_default())
        .expect("io");
    let base_set = hashes_of(base, content_defined);
    let mod_set = hashes_of(modified, content_defined);

    let novel = mod_set.difference(&base_set).count();
    let total_mod_bytes: u64 = mod_descs.iter().map(|d| d.length as u64).sum();
    let avg_len = total_mod_bytes as f64 / f64::from(u32::try_from(mod_set.len()).unwrap_or(1));
    (novel as f64 * avg_len) / total_mod_bytes as f64
}

fn base_with_middle_insertion(base_mb: usize, ins_mb: usize, salt: u64) -> (Vec<u8>, Vec<u8>) {
    // NOTE: the insertion is deliberately NOT a multiple of any fixed window
    // (1 MiB): an exactly window-aligned insertion shifts all subsequent
    // fixed-size blocks onto other existing block boundaries, which would
    // flatter fixed-size deduplication in a way real edits never do.
    let ins_len = ins_mb * 1024 * 1024 - 4099;
    let base = xorshift_payload(base_mb * 1024 * 1024, salt);
    let mut modified = Vec::with_capacity(base.len() + ins_len);
    modified.extend_from_slice(&base[..base.len() / 2]);
    modified.extend_from_slice(&xorshift_payload(ins_len, salt ^ 0xFACE));
    modified.extend_from_slice(&base[base.len() / 2..]);
    (base, modified)
}

#[test]
fn insertion_locality_beats_fixed_size_ci_scale() {
    let (base, modified) = base_with_middle_insertion(48, 2, 0xCAFE); // 48 MiB, +2 MiB

    let fastcdc = rehashed_fraction(&base, &modified, true);
    let fixed = rehashed_fraction(&base, &modified, false);

    eprintln!("fastcdc rehashed {fastcdc:.3}, fixed rehashed {fixed:.3}");
    assert!(
        fastcdc < 0.10,
        "fastcdc rehashed {:.1}% (want <10%)",
        fastcdc * 100.0
    );
    assert!(
        fixed > 0.45,
        "fixed-size should lose ~half its chunks after mid-file insert"
    );
    assert!(fastcdc * 3.0 < fixed, "want ≥3× improvement over fixed-size");
}

/// Full acceptance-spec variant: single 10 MiB insertion, <10% rehash.
#[test]
#[ignore = "heavy (~350 MiB RAM): run explicitly with `cargo test -- --ignored`"]
fn insertion_locality_full_spec() {
    let (base, modified) = base_with_middle_insertion(150, 10, 0xCAFE);
    let fastcdc = rehashed_fraction(&base, &modified, true);
    let fixed = rehashed_fraction(&base, &modified, false);
    eprintln!("full-spec: fastcdc {fastcdc:.4} fixed {fixed:.4}");
    assert!(fastcdc < 0.10, "fastcdc {:.2}%", fastcdc * 100.0);
    assert!(fixed > 0.45);
}

// ---------------------------------------------------------------------------
// 6. Size distribution
// ---------------------------------------------------------------------------

#[test]
fn chunk_size_distribution_matches_two_phase_model() {
    let data = xorshift_payload(96 * 1024 * 1024, 0x51CE);
    let descs = chunk_all(io::Cursor::new(&data), cfg()).expect("io");
    assert!(descs.len() > 50, "expected ~200+ chunks, got {}", descs.len());

    let lens: Vec<usize> = descs.iter().map(|d| d.length).collect();
    for (i, l) in lens.iter().enumerate() {
        if i + 1 < lens.len() {
            assert!(*l > MIN_CHUNK && *l <= MAX_CHUNK, "interior chunk {i}: len {l}");
        }
    }
    let mean = lens.iter().sum::<usize>() as f64 / lens.len() as f64;
    // Theory with the prompt's contiguous masks: E ≈ MIN + 2^18 ≈ 330 KiB
    // (documented deviation from nominal 1 MiB; docs/fastcdc.md §masks).
    assert!(
        (250.0 * 1024.0..=420.0 * 1024.0).contains(&mean),
        "mean chunk {mean:.0} outside model band"
    );
    assert!(
        lens.iter().any(|l| *l >= AVG_CHUNK),
        "phase 2 (MASK_L) never exercised"
    );
    assert!(
        descs
            .iter()
            .any(|d| d.content_features & CutPhase::PostAverage.feature_bit() != 0),
        "post-average feature bit never set"
    );
}

// ---------------------------------------------------------------------------
// 7. Reader robustness / fuzz-lite
// ---------------------------------------------------------------------------

#[test]
fn io_error_propagates() {
    struct Exploding;
    impl Read for Exploding {
        fn read(&mut self, _: &mut [u8]) -> io::Result<usize> {
            Err(io::Error::other("boom"))
        }
    }
    let mut s = FastCDCStream::new(Exploding).expect("cfg ok");
    assert!(s.next_chunk().is_err());
}

#[test]
fn fuzz_lite_random_truncations_never_panic() {
    // Deterministic pseudo-fuzz: random payloads around structural boundaries
    // through chunk_all; coverage and contiguity asserted throughout.
    let mut rng = StdRng::seed_from_u64(0xF00D_F00D);
    for case in 0..96u64 {
        let len = match rng.next_u64() % 6 {
            0 => jitter(&mut rng, 4096),
            1 => MIN_CHUNK - jitter(&mut rng, 100),
            2 => MIN_CHUNK + jitter(&mut rng, 500),
            3 => AVG_CHUNK - jitter(&mut rng, 1000),
            4 => AVG_CHUNK + jitter(&mut rng, 1000),
            _ => 4 * 1024 * 1024 - jitter(&mut rng, 100),
        };
        let mut data = vec![0u8; len];
        rng.fill_bytes(&mut data);
        let descs = chunk_all(io::Cursor::new(&data), cfg()).expect("fuzz io");
        let covered: usize = descs.iter().map(|d| d.length).sum();
        assert_eq!(covered, len, "case {case}: chunking lost/gained bytes");
        for w in descs.windows(2) {
            assert_eq!(
                w[0].offset + w[0].length as u64,
                w[1].offset,
                "case {case}: gap/overlap"
            );
        }
    }
}
