//! aegis-fastcdc — Content-Defined Chunking engine for Project Aegis.
//!
//! STATUS: environment scaffold (PROMPT 1.2). The Gear-hash cut-point search,
//! two-phase masking, and SIMD paths are delivered by PROMPT 3.1; this module
//! intentionally exposes only the stable contract surface so dependent
//! crates, benches, and property tests compile against fixed types today.
//!
//! Invariant served: **I-1 Bit-Perfect CAS** — every chunk leaving this crate
//! is addressed by the SHA-256 of its exact bytes (`ChunkDescriptor::hash`),
//! which is the sole identity used by the CAS registry and blob store.

/// Lower boundary: chunks never smaller than 64 KiB.
pub const MIN_CHUNK: usize = 64 * 1024;

/// Normalized target: ~1 MiB average chunk size.
pub const AVG_CHUNK: usize = 1024 * 1024;

/// Upper boundary: chunks never larger than 4 MiB.
pub const MAX_CHUNK: usize = 4 * 1024 * 1024;

/// Sub-average threshold mask (Gear fingerprint cut in `[MIN_CHUNK, AVG_CHUNK)`).
pub const MASK_S: u32 = 0x0003_FFFF;

/// Post-average threshold mask (Gear fingerprint cut in `[AVG_CHUNK, MAX_CHUNK]`).
pub const MASK_L: u32 = 0x0007_FFFF;

/// A content-addressed chunk produced by the CDC stream.
///
/// `hash` is `SHA-256(bytes[offset .. offset+length])` computed over the
/// source stream at emission time; downstream systems must treat
/// `(hash, length)` as the complete identity of the payload.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ChunkDescriptor {
    pub hash: [u8; 32],
    pub offset: u64,
    pub length: usize,
}

/// Errors surfaced by the chunking stream.
#[derive(Debug, thiserror::Error)]
pub enum ChunkError {
    #[error("source stream io failure: {0}")]
    Io(#[from] std::io::Error),
}

#[cfg(test)]
mod contract_tests {
    use super::*;

    #[test]
    fn bounds_are_ascending() {
        assert!(MIN_CHUNK < AVG_CHUNK && AVG_CHUNK < MAX_CHUNK);
    }

    #[test]
    fn descriptor_is_copy_and_hash_sized() {
        let d = ChunkDescriptor { hash: [7u8; 32], offset: 1, length: MIN_CHUNK };
        let c = d;
        assert_eq!(c.hash.len(), 32);
        assert_eq!(c.length, MIN_CHUNK);
    }
}
