//! Gear Matrix generation & verification tool.
//!
//! ```text
//! cargo run --release --example geargen                     # default matrix digest
//! cargo run --release --example geargen -- --seed 0x1234    # custom seed
//! cargo run --release --example geargen -- --verify-pin     # check shipped pin
//! cargo run --release --example geargen -- --preset small|large
//! ```
//!
//! The digest is SHA-256 over the 1024-byte little-endian serialization of
//! the table — the canonical way to verify a Gear matrix is byte-exact.

use aegis_fastcdc::gear::{generate_matrix, matrix_digest, presets, DEFAULT_SEED, GEAR_MATRIX};
use aegis_fastcdc::GEAR_MATRIX_SHA256;

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let mode = args.get(1).map_or("default", String::as_str);

    match mode {
        "--verify-pin" => {
            if matrix_digest(&GEAR_MATRIX) == *GEAR_MATRIX_SHA256 {
                println!("PIN OK: shipped GEAR_MATRIX matches {GEAR_MATRIX_SHA256}");
            } else {
                eprintln!("PIN MISMATCH — the static table drifted from the documented seed!");
                println!("actual:   {}", matrix_digest(&GEAR_MATRIX));
                println!("expected: {GEAR_MATRIX_SHA256}");
                std::process::exit(1);
            }
        }
        "--seed" => {
            let raw = args.get(2).cloned().unwrap_or_default();
            let hex = raw.trim_start_matches("0x");
            let seed = u64::from_str_radix(hex, 16).expect("--seed expects hex");
            print_matrix("custom", seed);
        }
        "--preset" => match args.get(2).map_or("", String::as_str) {
            "small" => print_matrix("small", presets::SMALL_SEED),
            "large" => print_matrix("large", presets::LARGE_SEED),
            other => {
                eprintln!("unknown preset {other:?} (want small | large)");
                std::process::exit(2);
            }
        },
        _ => print_matrix("default", DEFAULT_SEED),
    }
}

fn print_matrix(name: &str, seed: u64) {
    let m = generate_matrix(seed);
    println!("matrix   : {name}");
    println!("seed     : 0x{seed:016X}");
    println!("sha256(le): {}", matrix_digest(&m));
    println!("first 8  : {:?}", &m[..8]);
}
