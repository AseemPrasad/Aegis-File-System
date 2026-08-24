//! aegis-fastcdc — Content-Defined Chunking engine for Project Aegis.
//!
//! Implements `FastCDC` (Wen et al., USENIX ATC '16): Gear-hash rolling
//! fingerprint with **normalized two-phase masking**, producing
//! content-defined chunk boundaries that shift minimally under insertions —
//! the deduplication engine behind invariant **I-1 Bit-Perfect CAS** (every
//! chunk is addressed by the SHA-256 of its exact bytes).
//!
//! # Layout of an average-size chunk
//!
//! ```text
//! |<-- MIN_CHUNK -->|<--- phase 1: MASK_S --->|<--- phase 2: MASK_L --->|
//! 64 KiB            start scanning            AVG_CHUNK      hard stop
//!                    every byte                               MAX_CHUNK
//! ```
//!
//! * **Phase 1 (sub-average)** searches `[MIN_CHUNK, AVG_CHUNK)` with
//!   `MASK_S` (18 low bits). Its geometric stride (~2¹⁸ B ≈ 256 KiB) makes a
//!   boundary likely well before the average point, so most chunks terminate
//!   early and cheaply.
//! * **Phase 2 (post-average)** continues `[AVG_CHUNK, MAX_CHUNK]` with
//!   `MASK_L` (19 low bits, stride ~512 KiB) so survivors terminate long
//!   before the hard `MAX_CHUNK` backstop. Two masks exist because a mask
//!   dense enough to bound sizes tightly would *raise* the average; the
//!   normalized split keeps the distribution's mass low without ever
//!   sacrificing the size ceiling.
//!
//! # Why there is no cross-chunk fingerprint state
//!
//! Gear has no roll-out: the fingerprint resets to 0 at every boundary, so a
//! chunk's cut depends only on its own bytes. This is precisely what gives
//! insertion locality (a change re-chunks only its neighborhood) — carrying
//! fingerprint state across chunks would couple unrelated boundaries and is
//! deliberately absent from [`FastCDCStream`] (deviation from the original
//! task sketch, documented in docs/fastcdc.md).
//!
//! # SIMD
//!
//! The byte-serial recurrence is rewritten as an exact 8-wide vector identity
//! (`simd_x86.rs`, AVX2; `simd_arm.rs`, NEON) selected at runtime with a
//! scalar fallback. The `packed_simd` crate suggested in the original spec is
//! unmaintained and nightly-gated; stable `core::arch` intrinsics give the
//! same class of speedup with no nightly pin (rationale in docs/fastcdc.md).

use std::io::{self, Read};

use sha2::{Digest, Sha256};

pub mod gear;
pub mod scan;
#[cfg(target_arch = "aarch64")]
mod simd_arm;
#[cfg(target_arch = "x86_64")]
mod simd_x86;

pub use gear::{generate_matrix, GEAR_MATRIX, GEAR_MATRIX_SHA256};
pub use scan::{find_cut_scalar, find_cut_scalar_from};

/// Lower boundary: chunks never smaller than 64 KiB (except an EOF tail).
pub const MIN_CHUNK: usize = 64 * 1024;

/// Normalized target: ~1 MiB average chunk size.
pub const AVG_CHUNK: usize = 1024 * 1024;

/// Upper boundary: chunks never larger than 4 MiB.
pub const MAX_CHUNK: usize = 4 * 1024 * 1024;

/// Sub-average threshold mask (Gear fingerprint cut in `[MIN_CHUNK, AVG_CHUNK)`).
pub const MASK_S: u32 = 0x0003_FFFF;

/// Post-average threshold mask (Gear fingerprint cut in `[AVG_CHUNK, MAX_CHUNK]`).
pub const MASK_L: u32 = 0x0007_FFFF;

/// Which search phase produced a cut (see crate docs).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CutPhase {
    /// Hit during sub-average scan (`MASK_S`).
    SubAverage,
    /// Hit during post-average scan (`MASK_L`).
    PostAverage,
    /// Input ended before any mask hit — flushed remainder.
    EofTail,
    /// Hard ceiling reached with data still pending.
    HardMax,
}

impl CutPhase {
    /// Bit inside [`ChunkDescriptor::content_features`].
    #[must_use]
    pub const fn feature_bit(self) -> u32 {
        match self {
            CutPhase::SubAverage => FEATURE_PHASE1,
            CutPhase::PostAverage => FEATURE_PHASE2,
            CutPhase::EofTail => FEATURE_TRUNCATED,
            CutPhase::HardMax => FEATURE_HARD_MAX,
        }
    }
}

/// `content_features` bit: cut occurred in the sub-average phase.
pub const FEATURE_PHASE1: u32 = 1 << 24;
/// `content_features` bit: cut occurred in the post-average phase.
pub const FEATURE_PHASE2: u32 = 1 << 25;
/// `content_features` bit: input ended mid-chunk (tail shorter than needed).
pub const FEATURE_TRUNCATED: u32 = 1 << 26;
/// `content_features` bit: chunk clipped at `max_chunk` with data pending.
pub const FEATURE_HARD_MAX: u32 = 1 << 27;

/// Quantized Shannon entropy of the chunk payload, stored in
/// `content_features` bits 0..8 (0 = constant bytes, 255 ≈ 8 bits/byte).
///
/// # Panics
/// Panics if a histogram count exceeds `u32::MAX` — impossible for chunks
/// bounded by `max_chunk` on any realistic configuration.
#[must_use]
#[allow(
    clippy::cast_precision_loss,
    clippy::cast_possible_truncation,
    clippy::cast_sign_loss,
    reason = "entropy is bounded to [0,8] bits/byte; quantized value fits u32"
)]
pub fn entropy_q8(bytes: &[u8]) -> u32 {
    if bytes.is_empty() {
        return 0;
    }
    let mut hist = [0u64; 256];
    for &b in bytes {
        hist[usize::from(b)] += 1;
    }
    let n = bytes.len() as f64;
    let h: f64 = hist
        .iter()
        .filter(|&&c| c > 0)
        .map(|&c| {
            let p = f64::from(u32::try_from(c).expect("histogram count fits u32")) / n;
            -p * p.log2()
        })
        .sum();
    // Max entropy is log2(256) = 8 bits/byte ⇒ result ≤ 255.
    (((h / 8.0) * 255.0).round()) as u32
}

/// A content-addressed chunk produced by the CDC stream.
///
/// `hash` is `SHA-256(bytes[offset .. offset+length])` computed over the
/// source stream at emission time; downstream systems must treat
/// `(hash, length)` as the complete identity of the payload.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ChunkDescriptor {
    /// SHA-256 of the chunk payload — the CAS address (invariant I-1).
    pub hash: [u8; 32],
    /// Byte offset of the chunk within the input stream.
    pub offset: u64,
    /// Chunk size in bytes.
    pub length: usize,
    /// Analytics bits: low 8 = quantized entropy ([`entropy_q8`]), bits 24+
    /// = phase/edge flags ([`FEATURE_PHASE1`] etc.). Zero when the stream is
    /// configured with `compute_features = false`.
    pub content_features: u32,
}

/// Chunking parameters, including the Gear matrix and normalized masks.
///
/// Construct with [`Config::prompt_default`] (the PROMPT-specified 64K/1M/4M
/// regime) or [`Config::preset`]; all fields are public for custom regimes.
#[derive(Debug, Clone, Copy)]
pub struct Config {
    /// Minimum chunk size (phase-1 scan starts here).
    pub min_chunk: usize,
    /// Average/target size (phase switch point).
    pub avg_chunk: usize,
    /// Hard maximum chunk size.
    pub max_chunk: usize,
    /// Sub-average mask.
    pub mask_s: u32,
    /// Post-average mask.
    pub mask_l: u32,
    /// Gear lookup table (statically generated; see [`gear::generate_matrix`]).
    pub gear: &'static [u32; 256],
    /// Populate [`ChunkDescriptor::content_features`] (adds one histogram
    /// pass per chunk; disable in hot paths that don't consume analytics).
    pub compute_features: bool,
}

impl Config {
    /// The PROMPT 3.1 specification: 64 KiB / 1 MiB / 4 MiB, masks
    /// `0x0003_FFFF` / `0x0007_FFFF`, default Gear matrix.
    #[must_use]
    pub fn prompt_default() -> Self {
        Self {
            min_chunk: MIN_CHUNK,
            avg_chunk: AVG_CHUNK,
            max_chunk: MAX_CHUNK,
            mask_s: MASK_S,
            mask_l: MASK_L,
            gear: &GEAR_MATRIX,
            compute_features: true,
        }
    }

    /// Alternative regimes (masks sized so the expected sub-average stride
    /// lands near the target mean; derivation in docs/fastcdc.md §presets).
    #[must_use]
    pub fn preset(p: Preset) -> Self {
        match p {
            Preset::SmallBackup => {
                // 8 KiB min / 32 KiB avg: 15-bit mask → ~32 KiB stride.
                Self {
                    min_chunk: 8 * 1024,
                    avg_chunk: 32 * 1024,
                    max_chunk: 128 * 1024,
                    mask_s: 0x0000_7FFF,
                    mask_l: 0x0000_FFFF,
                    gear: &gear::presets::SMALL_MATRIX,
                    compute_features: true,
                }
            }
            Preset::LargeMedia => {
                // 1 MiB min / 8 MiB avg: 23-bit mask → ~8 MiB stride.
                Self {
                    min_chunk: 1024 * 1024,
                    avg_chunk: 8 * 1024 * 1024,
                    max_chunk: 64 * 1024 * 1024,
                    mask_s: 0x007F_FFFF,
                    mask_l: 0x00FF_FFFF,
                    gear: &gear::presets::LARGE_MATRIX,
                    compute_features: true,
                }
            }
        }
    }

    fn validate(&self) -> io::Result<()> {
        if self.min_chunk == 0 || self.avg_chunk <= self.min_chunk || self.max_chunk < self.avg_chunk
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "require min_chunk < avg_chunk <= max_chunk",
            ));
        }
        Ok(())
    }
}

/// Built-in chunking regimes.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Preset {
    /// Small-file backup workload (8 KiB / 32 KiB / 128 KiB).
    SmallBackup,
    /// Large media dedup (1 MiB / 8 MiB / 64 MiB).
    LargeMedia,
}

/// Streaming `FastCDC` chunker over any [`Read`] source.
///
/// Buffers up to `max_chunk` bytes, then performs the two-phase cut search
/// (SIMD-accelerated when available) and emits [`ChunkDescriptor`]s with
/// global offsets and SHA-256 addresses.
///
/// # Errors
/// Constructor forms return [`io::Error`] (`InvalidInput`) for degenerate
/// configs; `next_chunk` propagates reader failures.
#[derive(Debug)]
pub struct FastCDCStream<R: Read> {
    reader: R,
    buffer: Vec<u8>,
    /// Stream offset of `buffer[0]`.
    global_offset: u64,
    gear_matrix: &'static [u32; 256],
    cfg: Config,
    eof: bool,
}

impl<R: Read> FastCDCStream<R> {
    /// Create a stream with the PROMPT-specified defaults.
    ///
    /// # Errors
    /// Returns [`io::Error`] (`InvalidInput`) for degenerate configs.
    pub fn new(reader: R) -> io::Result<Self> {
        Self::with_config(reader, Config::prompt_default())
    }

    /// Create a stream with explicit configuration.
    ///
    /// # Errors
    /// Returns [`io::Error`] (`InvalidInput`) for degenerate configs.
    pub fn with_config(reader: R, cfg: Config) -> io::Result<Self> {
        cfg.validate()?;
        Ok(Self {
            reader,
            buffer: Vec::with_capacity(cfg.max_chunk + 64 * 1024),
            global_offset: 0,
            gear_matrix: cfg.gear,
            cfg,
            eof: false,
        })
    }

    /// Emit the next chunk descriptor, or `None` at clean end-of-stream.
    ///
    /// Edge cases (all covered by tests):
    /// * empty file → `Ok(None)`
    /// * file smaller than `min_chunk` → single tail chunk
    /// * stream ends mid-buffer → remainder flushed as final chunk
    /// * constant-byte runs → no mask hit is possible (`fp_n = GEAR[b]·(2ⁿ−1)`
    ///   with odd `(2ⁿ−1)`; see `scan.rs` test), so the hard `max_chunk`
    ///   backstop bounds them — work stays linear, chunks stay ≤ max.
    ///
    /// # Errors
    /// Propagates reader failures as [`ChunkError::Io`]-shaped
    /// [`std::io::Error`].
    pub fn next_chunk(&mut self) -> io::Result<Option<ChunkDescriptor>> {
        // Refill toward a full window unless the source is exhausted.
        self.refill()?;
        if self.buffer.is_empty() {
            return Ok(None); // clean EOF, empty input or fully consumed
        }

        let (cut_len, phase) = self.cut_point();
        let payload: Vec<u8> = self.buffer.drain(..cut_len).collect();

        let mut hasher = Sha256::new();
        hasher.update(&payload);
        let mut hash = [0u8; 32];
        hash.copy_from_slice(&hasher.finalize());

        let features = if self.cfg.compute_features {
            entropy_q8(&payload) | phase.feature_bit()
        } else {
            phase.feature_bit()
        };

        let desc = ChunkDescriptor {
            hash,
            offset: self.global_offset,
            length: payload.len(),
            content_features: features,
        };
        self.global_offset += payload.len() as u64;
        Ok(Some(desc))
    }

    /// Emit the next chunk boundary **without** hashing or feature
    /// computation — the pure cut-search path. This is what the
    /// `simd_speedup` A/B benchmark measures so the SIMD ratio is not
    /// masked by the SHA-256 floor.
    ///
    /// # Errors
    /// Propagates reader failures as [`std::io::Error`].
    pub fn next_boundary(&mut self) -> io::Result<Option<CutPoint>> {
        self.refill()?;
        if self.buffer.is_empty() {
            return Ok(None);
        }
        let (cut_len, _phase) = self.cut_point();
        self.buffer.drain(..cut_len);
        let point = CutPoint {
            offset: self.global_offset,
            length: cut_len,
        };
        self.global_offset += cut_len as u64;
        Ok(Some(point))
    }

    /// Read from the source until the window holds a full `max_chunk` or
    /// the stream hits EOF.
    fn refill(&mut self) -> io::Result<()> {
        let mut staging = vec![0u8; 256 * 1024];
        while !self.eof && self.buffer.len() < self.cfg.max_chunk {
            let n = self.reader.read(&mut staging)?;
            if n == 0 {
                self.eof = true;
            } else {
                self.buffer.extend_from_slice(&staging[..n]);
            }
        }
        Ok(())
    }

    /// Two-phase normalized cut search over the buffered window.
    fn cut_point(&mut self) -> (usize, CutPhase) {
        let cfg_min = self.cfg.min_chunk;
        let cap = self.buffer.len().min(self.cfg.max_chunk);
        let hard_max_pending = self.cfg.max_chunk == cap && !self.eof;

        if self.buffer.len() > cfg_min {
            let p1_end = cap.min(self.cfg.avg_chunk);
            if p1_end > cfg_min {
                if let Some(len) =
                    scan::find_cut(self.gear_matrix, &self.buffer[cfg_min..p1_end], self.cfg.mask_s)
                {
                    return (cfg_min + len, CutPhase::SubAverage);
                }
            }
            if cap > self.cfg.avg_chunk {
                if let Some(len) = scan::find_cut(
                    self.gear_matrix,
                    &self.buffer[self.cfg.avg_chunk..cap],
                    self.cfg.mask_l,
                ) {
                    return (self.cfg.avg_chunk + len, CutPhase::PostAverage);
                }
            }
        }

        if self.eof {
            (cap, CutPhase::EofTail)
        } else {
            debug_assert!(hard_max_pending);
            (cap, CutPhase::HardMax)
        }
    }

    /// Byte offset of the next chunk to be emitted.
    #[must_use]
    pub const fn stream_position(&self) -> u64 {
        self.global_offset
    }

    /// Release the inner reader.
    #[must_use]
    pub fn into_inner(self) -> R {
        self.reader
    }
}

/// Convenience: chunk an entire reader into a vector of descriptors.
///
/// # Errors
/// Propagates I/O errors from the source.
pub fn chunk_all<R: Read>(reader: R, cfg: Config) -> io::Result<Vec<ChunkDescriptor>> {
    let mut stream = FastCDCStream::with_config(reader, cfg)?;
    let mut out = Vec::new();
    while let Some(d) = stream.next_chunk()? {
        out.push(d);
    }
    Ok(out)
}

/// A chunk boundary without content addressing — `(offset, length)` of the
/// next region the scanner would emit.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct CutPoint {
    /// Byte offset of the chunk start within the stream.
    pub offset: u64,
    /// Chunk length in bytes.
    pub length: usize,
}

/// Convenience: scan an entire reader into cut points (no SHA-256, no
/// features). Used for pure throughput measurement and by callers that
/// hash asynchronously elsewhere.
///
/// # Errors
/// Propagates I/O errors from the source.
pub fn cuts_all<R: Read>(reader: R, cfg: Config) -> io::Result<Vec<CutPoint>> {
    let mut stream = FastCDCStream::with_config(reader, cfg)?;
    let mut out = Vec::new();
    while let Some(p) = stream.next_boundary()? {
        out.push(p);
    }
    Ok(out)
}

/// Name of the SIMD path runtime dispatch selected ("avx2", "neon", or
/// "scalar"). Used by benches/tests to label measurements honestly.
#[must_use]
pub fn active_simd_path() -> &'static str {
    #[cfg(target_arch = "x86_64")]
    {
        if std::is_x86_feature_detected!("avx2") {
            "avx2"
        } else {
            "scalar"
        }
    }
    #[cfg(target_arch = "aarch64")]
    {
        "neon"
    }
    #[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
    {
        "scalar"
    }
}

/// Force the SIMD cut search regardless of the env override — used by
/// correctness tests and A/B benchmarks (`simd_speedup` group).
///
/// # Panics
/// Panics on `x86_64` hosts without AVX2; call [`active_simd_path`] first.
#[must_use]
#[allow(unsafe_code)]
pub fn find_cut_simd_forced(gear: &'static [u32; 256], data: &[u8], mask: u32) -> Option<usize> {
    #[cfg(target_arch = "x86_64")]
    {
        assert!(
            std::is_x86_feature_detected!("avx2"),
            "find_cut_simd_forced requires AVX2"
        );
        // SAFETY: AVX2 verified immediately above.
        unsafe { simd_x86::find_cut_avx2(gear, data, mask) }
    }
    #[cfg(target_arch = "aarch64")]
    {
        simd_arm::find_cut_neon(gear, data, mask)
    }
    #[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
    {
        let _ = (gear, data, mask);
        panic!("no SIMD path compiled for this architecture")
    }
}

#[cfg(test)]
mod contract_tests {
    use super::*;

    #[test]
    #[allow(clippy::assertions_on_constants)] // the point is to pin them
    fn bounds_are_ascending() {
        assert!(MIN_CHUNK < AVG_CHUNK && AVG_CHUNK < MAX_CHUNK);
    }

    #[test]
    fn descriptor_is_copy_and_hash_sized() {
        let d = ChunkDescriptor {
            hash: [7u8; 32],
            offset: 1,
            length: MIN_CHUNK,
            content_features: 0,
        };
        let c = d;
        assert_eq!(c.hash.len(), 32);
        assert_eq!(c.length, MIN_CHUNK);
    }

    #[test]
    fn gear_default_matches_seed_and_digest_pin() {
        assert_eq!(gear::generate_matrix(gear::DEFAULT_SEED), GEAR_MATRIX);
        // Digest pin catches accidental PRNG/table changes in review.
        // Recompute with: cargo run --example geargen -- --verify-pin
        assert_eq!(gear::matrix_digest(&GEAR_MATRIX), *GEAR_MATRIX_SHA256);
    }

    #[test]
    fn mask_widths_match_documented_strides() {
        assert_eq!(MASK_S.trailing_ones(), 18); // 2^18 stride ≈ 256 KiB
        assert_eq!(MASK_L.trailing_ones(), 19); // 2^19 stride ≈ 512 KiB
    }
}
