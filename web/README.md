# Project Aegis — Full-Stack Web Workspace Console

This is the Next.js 14 App Router enterprise web application for **Project Aegis** (Exabyte-Capable Distributed CAS Storage Engine).

## Features Implemented

1. **Client-Side FastCDC Streaming Engine (`lib/fastcdc/fastcdc.ts`):**
   - Dynamic Content-Defined Chunking (2MB–8MB chunks) using precomputed Gear Hashing.
   - Parallel `SHA-256` block digest calculations via Web Crypto API.

2. **Pre-Signed HMAC Upload Engine (`lib/upload/upload-engine.ts`):**
   - Parallel multi-part S3/MinIO chunk uploads (concurrency pool size = 4).
   - Global client-side deduplication pre-filtering: skips uploading existing blocks found in backend CAS registry.

3. **Optimistic UI & State Management (`store/useFileStore.ts`):**
   - Zustand state store powering instant 0ms folder creation, renames, and batch deletions with automatic rollback handlers.

4. **Real-Time WebSocket Synchronization (`lib/socket/ws-client.ts`):**
   - Persistent WebSocket streaming client (`wss://api.aegis.internal/v1/ws`) with exponential backoff and 15-second heartbeat pings.
   - Live file status badge updates (`CLAMAV_SCANNING`, `OCR_PROCESSING`, `THUMBNAIL_READY`, `COMMIT_COMPLETE`).

5. **Multi-Tenant Navigation & Route Isolation (`app/[tenantId]/`):**
   - Workspace file tree navigator, CAS deduplication metrics dashboard, and tenant security key rotation console.

## Development Setup

```bash
# Navigate to web directory
cd web

# Install dependencies
npm install

# Run development server
npm run dev
```

The console will launch on `http://localhost:3000`.
