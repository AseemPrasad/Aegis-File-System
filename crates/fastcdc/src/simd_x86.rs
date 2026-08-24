//! AVX2 vectorized Gear cut search (`x86_64`).
//!
//! # The group identity
//!
//! The Gear recurrence is plain integer arithmetic mod 2³²:
//! `h_{t+1} = (h_t << 1) + GEAR[b_t]`. Unrolling eight steps and reducing
//! with ordinary algebra (wrapping = ring ℤ/2³², so distribution is exact):
//!
//! ```text
//! h_{t+k} = (h_t << k) + Σ_{j<k} GEAR[b_{t+j}] << (k-1-j)      (mod 2^32)
//! ```
//!
//! Define the suffix sums `s_k = Σ_{j≤k} GEAR[b_{t+j}] << (k-j)` — a chain of
//! 8 cheap adds (`s_k = (s_{k-1} << 1) + GEAR[b_{t+k}]`). Then all eight
//! candidate fingerprints in an 8-byte group are:
//!
//! ```text
//! lane_k = (fp_running * 2^{k+1}) + s_k        k = 0..7
//! ```
//!
//! which is ONE vector multiply by the constant vector [2,4,...,256], one
//! vector add of the `s` array, one AND+CMPEQ against the mask, and one
//! MOVEMASK. Boundary = first (lowest-lane) hit via `TZCNT`, matching
//! scalar's earliest-position semantics exactly. After a clean group the
//! recurrence folds to `fp_next = (fp << 8) + s_7` — bit-identical to scalar
//! by construction.
//!
//! # Why the suffix chains stay scalar (measured, not guessed)
//!
//! Building `s` fully in vector registers (Hillis–Steele weighted prefix
//! scan) was implemented and benchmarked: on Zen 2 it *regressed* from
//! 29.7 ms → 40.7 ms per 64 MiB because the three required cross-lane
//! `vpermd`s plus the extra blends/mullos outweigh the 24 scalar µops they
//! replace, while `vpgatherdd` table lookups are microcoded-slow. The
//! shipped design instead runs TWO INDEPENDENT scalar suffix chains (one
//! per 8-byte half of a 16-byte pair), so the two chains interleave on the
//! out-of-order engine and the only serial dependency between pairs is the
//! single `fp` fold — measured fastest on this microarchitecture. The HS
//! derivation is preserved in docs/fastcdc.md §"SIMD formulation" for
//! re-evaluation on cores where cross-lane permutes are single-µop (Ice
//! Lake+, Zen 4+).
//!
//! Full derivation mirrored in docs/fastcdc.md §"SIMD formulation".

#![allow(unsafe_code)]
// SAFETY (module-wide note): every function below is gated behind
// #[target_feature(enable = "avx2")] and is called only from scan.rs after
// is_x86_feature_detected!("avx2") returns true. All intrinsics operate on
// stack arrays or borrowed slices; loads use unaligned variants; nothing
// aliases.

use std::arch::x86_64::{
    _mm256_add_epi32, _mm256_and_si256, _mm256_castsi256_ps, _mm256_cmpeq_epi32,
    _mm256_loadu_si256, _mm256_movemask_ps, _mm256_mullo_epi32, _mm256_set1_epi32,
    _mm256_setr_epi32, _mm256_setzero_si256,
};

/// AVX2 cut search over `data`; called from `scan::find_cut` only.
///
/// # Safety
/// Caller must ensure AVX2 CPU support (checked via runtime detection).
// The u32↔i32 lane casts below are bit-identical reinterpretations: every
// operation used (mullo/add/and/cmp) is sign-agnostic on 32-bit lanes.
#[allow(
    clippy::cast_possible_wrap,
    clippy::cast_sign_loss,
    reason = "SIMD lanes are raw 32-bit patterns"
)]
#[target_feature(enable = "avx2")]
pub(crate) unsafe fn find_cut_avx2(
    gear: &[u32; 256],
    data: &[u8],
    mask: u32,
) -> Option<usize> {
    let mask_v = _mm256_set1_epi32(mask as i32);
    // Lane k multiplies the running fp by 2^(k+1): the `(fp << (k+1))` term.
    let pow_v = _mm256_setr_epi32(2, 4, 8, 16, 32, 64, 128, 256);
    let zero = _mm256_setzero_si256();

    let mut fp: u32 = 0; // chunk-local: Gear has no roll-out, fp resets at boundaries
    let mut pairs = data.chunks_exact(16);
    let mut consumed = 0usize;

    for pair in &mut pairs {
        // Two INDEPENDENT suffix-sum chains (one per 8-byte half): they
        // interleave out-of-order and the serial critical path stays 8 steps
        // per 16 bytes. See module docs for why this beats an all-vector
        // Hillis-Steele construction on Zen 2.
        let mut s_lo = [0u32; 8];
        let mut s_hi = [0u32; 8];
        let mut acc_lo = 0u32;
        let mut acc_hi = 0u32;
        for k in 0..8 {
            acc_lo = (acc_lo << 1).wrapping_add(gear[usize::from(pair[k])]);
            s_lo[k] = acc_lo;
            acc_hi = (acc_hi << 1).wrapping_add(gear[usize::from(pair[k + 8])]);
            s_hi[k] = acc_hi;
        }

        // SAFETY: AVX2 feature verified by caller; stack-array loads use
        // unaligned-load intrinsics.
        let hv_lo = _mm256_add_epi32(
            _mm256_mullo_epi32(_mm256_set1_epi32(fp as i32), pow_v),
            _mm256_loadu_si256(s_lo.as_ptr().cast()),
        );
        let bits_lo = _mm256_movemask_ps(_mm256_castsi256_ps(
            _mm256_cmpeq_epi32(_mm256_and_si256(hv_lo, mask_v), zero),
        )) as u32;
        if bits_lo != 0 {
            // Lowest set lane = earliest position = first cut, matching
            // scalar order-of-scan semantics exactly.
            let lane =
                usize::try_from(bits_lo.trailing_zeros()).expect("movemask yields bits 0..=7");
            return Some(consumed + lane + 1);
        }
        let fp_mid = (fp << 8).wrapping_add(s_lo[7]);

        let hv_hi = _mm256_add_epi32(
            _mm256_mullo_epi32(_mm256_set1_epi32(fp_mid as i32), pow_v),
            _mm256_loadu_si256(s_hi.as_ptr().cast()),
        );
        let bits_hi = _mm256_movemask_ps(_mm256_castsi256_ps(
            _mm256_cmpeq_epi32(_mm256_and_si256(hv_hi, mask_v), zero),
        )) as u32;
        if bits_hi != 0 {
            let lane =
                usize::try_from(bits_hi.trailing_zeros()).expect("movemask yields bits 0..=7");
            return Some(consumed + 8 + lane + 1);
        }
        fp = (fp_mid << 8).wrapping_add(s_hi[7]);
        consumed += 16;
    }

    let rest = pairs.remainder();
    if rest.is_empty() {
        None
    } else {
        crate::scan::find_cut_scalar_from(gear, rest, mask, fp).map(|len| consumed + len)
    }
}
