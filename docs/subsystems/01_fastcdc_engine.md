# Subsystem Spec 01: FastCDC Chunker Engine

## 1. Overview
The **FastCDC Chunker Engine** (`crates/fastcdc`) is a high-throughput, SIMD-accelerated content-defined chunking module written in Rust (Edition 2021). It dynamically splits arbitrary binary payload streams into chunks based on content properties rather than fixed byte offsets.

---

## 2. Mathematical & Algorithmic Mechanics

### Gear Hashing
FastCDC uses a pre-computed 256-entry 32-bit Gear hash table (`GEAR_TABLE`) to compute rolling fingerprints over a sliding byte window:

$$\text{Fingerprint}_{i} = (\text{Fingerprint}_{i-1} \ll 1) + \text{GEAR}[B_i]$$

### Dynamic Boundary Thresholds
When evaluating byte positions, a bitmask check triggers dynamic chunk boundary cuts:
- Minimum Chunk Size: $64\text{ KB}$ (no cuts evaluated below this threshold).
- Average Target Chunk Size: $256\text{ KB} - 1\text{ MB}$.
- Maximum Chunk Size: $8\text{ MB}$ (hard cut forced if no boundary is found).

---

## 3. Go FFI / Cgo Binding Interface
The Go ingress engine communicates with the Rust chunker via Cgo bindings (`internal/fastcdc`), passing pointer references to C byte arrays:

```rust
#[no_mangle]
pub extern "C" fn fastcdc_chunk_buffer(
    data: *const u8,
    len: usize,
    min_size: usize,
    avg_size: usize,
    max_size: usize,
    out_chunks: *mut ChunkResult,
) -> i32
```
