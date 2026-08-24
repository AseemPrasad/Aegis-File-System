//! ARM NEON vectorized Gear cut search (aarch64).
//!
//! Identical group identity as the AVX2 module (see its docs), at 4 lanes:
//!
//! ```text
//! lane_k = (fp_running * 2^{k+1}) + s_k        k = 0..3
//! fp_next = (fp_running << 4) + s_3
//! ```
//!
//! NEON is baseline on aarch64, so no runtime detection or unsafe is needed;
//! `core::arch::aarch64` intrinsics are safe functions. Group width is 4
//! because `uint32x4_t` holds four u32 lanes; the scalar tail handles the
//! remainder plus short windows.

use std::arch::aarch64::{
    vaddq_u32, vceqq_u32, vdupq_n_u32, vgetq_lane_u32, vld1q_u32, vmulq_u32,
};

/// NEON cut search over `data`.
pub(crate) fn find_cut_neon(gear: &[u32; 256], data: &[u8], mask: u32) -> Option<usize> {
    let mask_v = vdupq_n_u32(mask);
    // Lane k multiplies the running fp by 2^(k+1).
    let pow_v = vld1q_u32(&[2, 4, 8, 16]);
    let zero = vdupq_n_u32(0);

    let mut fp: u32 = 0;
    let mut groups = data.chunks_exact(4);
    let mut consumed = 0usize;

    for group in &mut groups {
        let mut s = [0u32; 4];
        let mut acc = 0u32;
        for (k, &byte) in group.iter().enumerate() {
            acc = (acc << 1).wrapping_add(gear[usize::from(byte)]);
            s[k] = acc;
        }

        let hv = vaddq_u32(
            vmulq_u32(vdupq_n_u32(fp), pow_v),
            vld1q_u32(&s),
        );
        let hits = vceqq_u32(hv & mask_v, zero);
        if vgetq_lane_u32(hits, 0) != 0 {
            return Some(consumed + 1);
        }
        if vgetq_lane_u32(hits, 1) != 0 {
            return Some(consumed + 2);
        }
        if vgetq_lane_u32(hits, 2) != 0 {
            return Some(consumed + 3);
        }
        if vgetq_lane_u32(hits, 3) != 0 {
            return Some(consumed + 4);
        }
        fp = (fp << 4).wrapping_add(s[3]);
        consumed += 4;
    }

    let rest = groups.remainder();
    if rest.is_empty() {
        None
    } else {
        crate::scan::find_cut_scalar_from(gear, rest, mask, fp).map(|len| consumed + len)
    }
}
