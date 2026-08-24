//! Cut-point search: scalar reference implementation (also the oracle for
//! tests) plus runtime dispatch to SIMD paths.
//!
//! Contract of `find_cut`: given `data` beginning at a chunk start with
//! `fp == 0`, return the chunk length — i.e. consume bytes one at a time,
//! `fp = (fp << 1).wrapping_add(GEAR[byte])`, stop after the first byte where
//! `(fp & mask) == 0`. Never returns 0; returns `None` if no boundary exists
//! within `data`.

/// Scalar byte-at-a-time Gear scan. This is the normative definition of the
/// algorithm; the SIMD paths must produce bit-identical results
/// (`tests/oracle.rs::scalar_simd_equivalence`).
#[must_use]
pub fn find_cut_scalar(gear: &[u32; 256], data: &[u8], mask: u32) -> Option<usize> {
    find_cut_scalar_from(gear, data, mask, 0)
}

/// Same recurrence seeded with a caller-supplied fingerprint — used by the
/// SIMD paths to finish their tail bytes with exact cross-group state.
#[must_use]
pub fn find_cut_scalar_from(
    gear: &[u32; 256],
    data: &[u8],
    mask: u32,
    fp_init: u32,
) -> Option<usize> {
    let mut fp: u32 = fp_init;
    for (pos, &byte) in data.iter().enumerate() {
        fp = (fp << 1).wrapping_add(gear[usize::from(byte)]);
        if fp & mask == 0 {
            return Some(pos + 1);
        }
    }
    None
}

/// Runtime-dispatching cut search. Selection order on `x86_64`: forced-scalar
/// (env `AEGIS_FASTCDC_FORCE_SCALAR=1`) > AVX2 > scalar. On `aarch64`: NEON >
/// scalar. The branch predictor cost is one predictable call per chunk.
#[must_use]
#[allow(unsafe_code)]
pub fn find_cut(gear: &'static [u32; 256], data: &[u8], mask: u32) -> Option<usize> {
    #[cfg(target_arch = "x86_64")]
    {
        if !force_scalar() && std::is_x86_feature_detected!("avx2") {
            // SAFETY: avx2 feature verified immediately above; the function
            // only reads `gear`/`data` and writes stack locals.
            return unsafe { crate::simd_x86::find_cut_avx2(gear, data, mask) };
        }
    }
    #[cfg(target_arch = "aarch64")]
    {
        if !force_scalar() {
            return crate::simd_arm::find_cut_neon(gear, data, mask);
        }
    }
    find_cut_scalar(gear, data, mask)
}

fn force_scalar() -> bool {
    static FORCE: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
    *FORCE.get_or_init(|| {
        matches!(
            std::env::var("AEGIS_FASTCDC_FORCE_SCALAR"),
            Ok(ref v) if v == "1" || v.eq_ignore_ascii_case("true")
        )
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn constant_input_never_masks_but_stays_bounded() {
        // Mathematical fact, verified here as a regression guard:
        // over constant bytes, fp_n = GEAR[b]·(2^n − 1). Since (2^n − 1) is
        // odd, the low 18 bits are all zero only if 2^18 | GEAR[b], which no
        // table entry satisfies by construction. Constant runs therefore
        // terminate via the MAX_CHUNK backstop, not a mask hit.
        let gear = &crate::gear::GEAR_MATRIX;
        assert!(crate::gear::GEAR_MATRIX[0] % 262_144 != 0);
        let data = vec![0u8; 2 * 1024 * 1024]; // ~8× the random-data stride
        assert_eq!(find_cut_scalar(gear, &data, crate::MASK_S), None);
    }

    #[test]
    #[allow(clippy::cast_possible_truncation)] // xorshift byte extraction
    fn random_data_hits_well_within_stride() {
        let gear = &crate::gear::GEAR_MATRIX;
        let mut rng: u64 = 0x5EED;
        let mut data = vec![0u8; 1024 * 1024];
        for b in &mut data {
            rng ^= rng << 13;
            rng ^= rng >> 7;
            rng ^= rng << 17;
            *b = rng as u8;
        }
        let cut = find_cut_scalar(gear, &data, crate::MASK_S)
            .expect("random data cuts within ~2^18 bytes on average");
        assert!((1..=data.len()).contains(&cut));
    }

    #[test]
    #[allow(clippy::cast_possible_truncation)] // xorshift byte extraction
    fn none_when_no_boundary_in_window() {
        // A window this small almost surely contains no 18-bit match.
        let gear = &crate::gear::GEAR_MATRIX;
        let mut rng: u64 = 0x5EED;
        let data: Vec<u8> = (0..64)
            .map(|_| {
                rng ^= rng << 13;
                rng ^= rng >> 7;
                rng ^= rng << 17;
                rng as u8
            })
            .collect();
        assert_eq!(find_cut_scalar(gear, &data, crate::MASK_S), None);
    }
}
