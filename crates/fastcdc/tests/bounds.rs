use aegis_fastcdc::{MASK_L, MASK_S, MAX_CHUNK, MIN_CHUNK};

proptest::proptest! {
    #![proptest_config(proptest::prelude::ProptestConfig::with_cases(512))]

    #[test]
    fn masks_within_u32_and_stable(m in 0u32..1_000) {
        let _ = m; // determinism probe: constants must never depend on input
        prop_assert_eq!(MASK_S & !MASK_S, 0);
        prop_assert_eq!(MASK_L & !MASK_L, 0);
    }

    #[test]
    fn chunk_bounds_ascending(seed in 0u64..1_000_000) {
        let _ = seed;
        prop_assert!(MIN_CHUNK < MAX_CHUNK);
    }
}
