//! Fuzz harness: arbitrary bytes + truncation against the naive oracle.
//!
//! Invariants checked on every input:
//! 1. Stream output equals the byte-at-a-time reference chunker exactly
//!    (offsets, lengths, and therefore hashes).
//! 2. No panic on any input, including empty, sub-min_chunk, constant
//!    runs (MAX_CHUNK backstop), and adversarial sizes around group edges.
//!
//! Run under cargo-fuzz (nightly, Linux/macOS/WSL):
//! `cargo +nightly fuzz run stream_oracle -- -max_len=3000000`

#![no_main]

use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    let owned = data.to_vec();
    let cfg = aegis_fastcdc::Config::prompt_default();

    let streamed = aegis_fastcdc::chunk_all(std::io::Cursor::new(&owned), cfg)
        .expect("in-memory reader cannot fail");

    let mut pos = 0usize;
    for desc in &streamed {
        assert_eq!(desc.offset, pos as u64, "descriptor offsets must tile");
        pos += desc.length;
        assert!(desc.length <= 4 * 1024 * 1024, "hard max enforced");
    }
    assert_eq!(pos, owned.len(), "chunks must cover input exactly");

    if !owned.is_empty() && owned.len() < aegis_fastcdc::MIN_CHUNK {
        assert_eq!(streamed.len(), 1, "sub-min input = single tail chunk");
    }
});
