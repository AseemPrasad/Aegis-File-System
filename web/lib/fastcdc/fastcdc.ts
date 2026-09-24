// Web Worker script for streaming FastCDC chunking & parallel SHA-256 calculation
export interface ChunkResult {
  hash: string;
  offset: number;
  size: number;
  data: Uint8Array;
}

const MIN_CHUNK_SIZE = 2 * 1024 * 1024; // 2MB
const MAX_CHUNK_SIZE = 8 * 1024 * 1024; // 8MB
const TARGET_CHUNK_SIZE = 4 * 1024 * 1024; // 4MB
const GEAR_SEED = 0x89abcdef;

// Precomputed Gear Table for FastCDC boundary detection
const GEAR_TABLE = new Uint32Array(256);
for (let i = 0; i < 256; i++) {
  let h = i * 2654435761;
  h = Math.imul(h ^ (h >>> 16), 2246822507);
  h = Math.imul(h ^ (h >>> 13), 3266489917);
  GEAR_TABLE[i] = (h ^ (h >>> 16)) >>> 0;
}

export async function computeSHA256(buffer: Uint8Array): Promise<string> {
  const hashBuffer = await crypto.subtle.digest('SHA-256', buffer.buffer as ArrayBuffer);
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  return hashArray.map((b) => b.toString(16).padStart(2, '0')).join('');
}

export async function chunkBufferFastCDC(
  buffer: Uint8Array,
  onChunkReady?: (chunk: ChunkResult) => void
): Promise<ChunkResult[]> {
  const chunks: ChunkResult[] = [];
  const len = buffer.length;
  let offset = 0;

  while (offset < len) {
    const remaining = len - offset;
    if (remaining <= MIN_CHUNK_SIZE) {
      const slice = buffer.subarray(offset, len);
      const hash = await computeSHA256(slice);
      const chunk: ChunkResult = { hash, offset, size: slice.length, data: slice };
      chunks.push(chunk);
      if (onChunkReady) onChunkReady(chunk);
      break;
    }

    let chunkSize = MIN_CHUNK_SIZE;
    const maxScan = Math.min(remaining, MAX_CHUNK_SIZE);
    let fingerprint = 0;

    // Dynamic Content-Defined Boundary Scan (Gear Hashing)
    for (; chunkSize < maxScan; chunkSize++) {
      const byte = buffer[offset + chunkSize];
      fingerprint = ((fingerprint << 1) + GEAR_TABLE[byte]) >>> 0;
      // Mask threshold triggers dynamic cut boundary
      if ((fingerprint & 0x000fffff) === 0) {
        break;
      }
    }

    const slice = buffer.subarray(offset, offset + chunkSize);
    const hash = await computeSHA256(slice);
    const chunk: ChunkResult = { hash, offset, size: chunkSize, data: slice };
    chunks.push(chunk);
    if (onChunkReady) onChunkReady(chunk);

    offset += chunkSize;
  }

  return chunks;
}
