//! Gear matrix generation and the built-in tables.
//!
//! The Gear hash is `fp = (fp << 1).wrapping_add(GEAR[byte])`. Its boundary
//! statistics only require that the 256 table entries behave like uniform
//! random u32 values; any fixed, reproducible PRNG works. We use the MMIX
//! LCG (Donald Knuth, TAOCP vol. 2) run on `u64` state with a `SplitMix64`
//! finalizer per output word:
//!
//! ```text
//! state = seed
//! step():  state = state * 6364136223846793005 + 1442695040888963407
//! out():   z = step(); z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9;
//!          z = (z ^ (z >> 27)) * 0x94D049BB133111EB; z ^ (z >> 31)
//! ```
//!
//! The finalizer decorrelates consecutive LCG outputs so low-order-bit
//! artifacts of the raw LCG cannot bias the mask match probability.
//!
//! Verification is by digest: `SHA-256(gear_bytes_le)` over the 1024-byte
//! little-endian serialization of the table. `geargen` (examples/) prints it;
//! `GEAR_MATRIX_SHA256` pins the shipped default; a unit test regenerates the
//! table from the documented seed and asserts byte-equality with the static.

/// Default Gear matrix. Generated from seed `0xAEG1`-mnenomic
/// `0x0000_AE61_5CDC_0001` ("aegis fastcdc 1") via [`generate_matrix`].
pub static GEAR_MATRIX: [u32; 256] = generate_matrix(DEFAULT_SEED);

/// Seed for [`GEAR_MATRIX`]. Changing this changes every chunk boundary ever
/// produced; treat as immutable protocol state (documented in docs/fastcdc.md).
pub const DEFAULT_SEED: u64 = 0x0000_AE61_5CDC_0001;

/// SHA-256 of the little-endian bytes of [`GEAR_MATRIX`] — printed by
/// `cargo run --example geargen`, asserted by `gear_default_matches_seed`.
pub const GEAR_MATRIX_SHA256: &str =
    "c67245237aa1be2ad954ffaf5bc1479deb79ca3cc1dbb99783f2fe2c68cf1a32";

/// Generate a 256-entry Gear matrix from a 64-bit seed.
///
/// Deterministic across platforms and releases; the PRNG above has no
/// platform-dependent behavior (all arithmetic is wrapping u64). `const` so
/// preset tables are generated at compile time.
#[must_use]
#[allow(clippy::cast_possible_truncation)] // low 32 bits are the point
pub const fn generate_matrix(seed: u64) -> [u32; 256] {
    let mut state = seed;
    let mut out = [0u32; 256];
    // const fn cannot iterate via IntoIterator; plain index loop instead.
    let mut i = 0;
    while i < out.len() {
        state = state
            .wrapping_mul(6_364_136_223_846_793_005)
            .wrapping_add(1_442_695_040_888_963_407);
        let mut z = state;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        out[i] = (z ^ (z >> 31)) as u32;
        i += 1;
    }
    out
}

/// SHA-256 hex digest of a matrix's little-endian serialization.
#[must_use]
pub fn matrix_digest(matrix: &[u32; 256]) -> String {
    use sha2::{Digest, Sha256};
    use std::fmt::Write as _;
    let mut bytes = [0u8; 1024];
    for (i, word) in matrix.iter().enumerate() {
        bytes[i * 4..i * 4 + 4].copy_from_slice(&word.to_le_bytes());
    }
    let digest = Sha256::digest(bytes);
    let mut hex = String::with_capacity(64);
    for b in digest {
        let _ = write!(hex, "{b:02x}");
    }
    hex
}

/// Alternative matrices for different chunk-size regimes. Each preset pairs a
/// matrix seed with normalized masks whose bit widths target the regime's
/// average size (see `Config::preset`). Seeds are distinct from
/// [`super::DEFAULT_SEED`] so no two presets can ever produce identical
/// boundaries on shared data.
pub mod presets {
    use super::generate_matrix;

    /// Small-file backup workload: 8 KiB / 32 KiB / 128 KiB.
    pub const SMALL_SEED: u64 = 0x0000_AE61_5CDC_0032;
    /// Matrix behind [`crate::Preset::SmallBackup`].
    pub static SMALL_MATRIX: [u32; 256] = generate_matrix(SMALL_SEED);

    /// Default prompt spec: 64 KiB / 1 MiB / 4 MiB (uses [`super::DEFAULT_SEED`]).
    pub const DEFAULT_TARGET_MIN: usize = 64 * 1024;
    /// See [`Self::DEFAULT_TARGET_MIN`].
    pub const DEFAULT_TARGET_AVG: usize = 1024 * 1024;
    /// See [`Self::DEFAULT_TARGET_MIN`].
    pub const DEFAULT_TARGET_MAX: usize = 4 * 1024 * 1024;

    /// Large media dedup: 1 MiB / 8 MiB / 64 MiB.
    pub const LARGE_SEED: u64 = 0x0000_AE61_5CDC_0800;
    /// Matrix behind [`crate::Preset::LargeMedia`].
    pub static LARGE_MATRIX: [u32; 256] = generate_matrix(LARGE_SEED);
}
