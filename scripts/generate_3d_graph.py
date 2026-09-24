#!/usr/bin/env python3
"""
Generate 3D Architectural Graph Data for Project Aegis Visualizer.
Scans repository directories and outputs a structured JSON graph.
"""

import os
import json

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
OUTPUT_PATH = os.path.join(REPO_ROOT, "docs", "visualizer", "graph_data.json")

# Define Subsystem Categories
SUBSYSTEMS = {
    "crates/fastcdc": {"name": "Rust SIMD FastCDC Engine", "category": "rust", "color": "#e056fd", "tier": 1},
    "internal/cas": {"name": "Content-Addressed Storage Engine", "category": "go_core", "color": "#00d2d3", "tier": 2},
    "internal/ingress": {"name": "Stateless HTTP Ingress & Auth", "category": "go_ingress", "color": "#54a0ff", "tier": 2},
    "internal/database": {"name": "PostgreSQL Metadata & ltree", "category": "db", "color": "#5f27cd", "tier": 3},
    "internal/objectstorage": {"name": "S3/MinIO CAS Blob Storage", "category": "storage", "color": "#1dd1a1", "tier": 3},
    "internal/cdc": {"name": "Debezium WAL CDC Stream", "category": "events", "color": "#ff9f43", "tier": 4},
    "internal/workers": {"name": "Async Derivation Workers", "category": "workers", "color": "#ff6b6b", "tier": 4},
    "internal/auth": {"name": "KMS & Envelope Encryption", "category": "security", "color": "#feca57", "tier": 2},
    "internal/billing": {"name": "Multi-Tenant Metering & Quotas", "category": "billing", "color": "#48dbfb", "tier": 2},
    "internal/gc": {"name": "Garbage Collection & Sweeper", "category": "gc", "color": "#ff9ff3", "tier": 3},
    "web": {"name": "Next.js Web Dashboard & Wasm", "category": "frontend", "color": "#00d2d3", "tier": 1},
    "db/migrations": {"name": "SQL Schemas & 16 Hash Partitions", "category": "db_schema", "color": "#341f97", "tier": 3}
}

def scan_repo():
    nodes = []
    links = []
    node_id_map = {}

    # Core Hub Nodes
    hubs = [
        {"id": "hub_client", "name": "Web Client / Edge SDK", "category": "client", "color": "#00d2d3", "val": 25, "tier": 0, "desc": "Wasm FastCDC + Direct Signed Uploads"},
        {"id": "hub_ingress", "name": "Go Ingress Server Cluster", "category": "ingress", "color": "#54a0ff", "val": 30, "tier": 1, "desc": "Stateless Auth, Session Generation & Commit API"},
        {"id": "hub_rust_fastcdc", "name": "Rust SIMD FastCDC Engine", "category": "rust", "color": "#e056fd", "val": 28, "tier": 1, "desc": "Rolling hash content-defined chunking (1MB-8MB)"},
        {"id": "hub_postgres", "name": "PostgreSQL 16 Cluster (ltree + Partitions)", "category": "db", "color": "#5f27cd", "val": 35, "tier": 2, "desc": "Sub-ms directory trees & 16-partition cas_blocks"},
        {"id": "hub_redis", "name": "Redis 7 Cluster (Rate-Limit & Locks)", "category": "cache", "color": "#ff4757", "val": 22, "tier": 2, "desc": "Distributed token bucket limiters & atomic session locks"},
        {"id": "hub_s3", "name": "Object Storage (S3 / MinIO / Ceph)", "category": "storage", "color": "#1dd1a1", "val": 35, "tier": 3, "desc": "Immutable SHA-256 CAS blob store"},
        {"id": "hub_cdc", "name": "Debezium CDC + Kafka Event Bus", "category": "events", "color": "#ff9f43", "val": 26, "tier": 3, "desc": "WAL event streaming for zero-latency commits"},
        {"id": "hub_workers", "name": "Async Worker Fleet", "category": "workers", "color": "#ff6b6b", "val": 24, "tier": 4, "desc": "ClamAV scanning, Thumbnails, EXIF & Search Indexing"}
    ]

    for hub in hubs:
        nodes.append(hub)
        node_id_map[hub["id"]] = hub

    # Hub Connections
    hub_links = [
        {"source": "hub_client", "target": "hub_ingress", "label": "HTTP/2 Session & Commit Requests", "value": 5},
        {"source": "hub_client", "target": "hub_s3", "label": "Direct Presigned Put Object", "value": 8},
        {"source": "hub_ingress", "target": "hub_rust_fastcdc", "label": "CGo/FFI SIMD Chunking", "value": 6},
        {"source": "hub_ingress", "target": "hub_redis", "label": "Quota & Token Bucket Lock", "value": 4},
        {"source": "hub_ingress", "target": "hub_postgres", "label": "ltree Tree Insert & CAS Dedup Query", "value": 7},
        {"source": "hub_postgres", "target": "hub_cdc", "label": "PostgreSQL WAL Replication Stream", "value": 6},
        {"source": "hub_cdc", "target": "hub_workers", "label": "Event Fan-out (NATS/Kafka)", "value": 5},
        {"source": "hub_workers", "target": "hub_s3", "label": "Read Original / Write Derivations", "value": 4},
        {"source": "hub_workers", "target": "hub_postgres", "label": "Update Derivation Metadata", "value": 4}
    ]

    for link in hub_links:
        links.append(link)

    # Walk directory tree for specific files
    for root, dirs, files in os.walk(REPO_ROOT):
        if any(ignored in root for ignored in [".git", "target", "node_modules", ".next"]):
            continue

        rel_dir = os.path.relpath(root, REPO_ROOT).replace("\\", "/")

        for f in files:
            if f.endswith((".go", ".rs", ".ts", ".tsx", ".sql")):
                file_path = os.path.join(rel_dir, f).replace("\\", "/")
                file_size = os.path.getsize(os.path.join(root, f))
                
                # Determine subsystem
                matched_sub = "go_core"
                matched_color = "#70a1ff"
                parent_hub = "hub_ingress"

                if "crates/fastcdc" in file_path:
                    matched_sub = "rust"
                    matched_color = "#e056fd"
                    parent_hub = "hub_rust_fastcdc"
                elif "internal/database" in file_path or "db/migrations" in file_path:
                    matched_sub = "db"
                    matched_color = "#5f27cd"
                    parent_hub = "hub_postgres"
                elif "internal/objectstorage" in file_path:
                    matched_sub = "storage"
                    matched_color = "#1dd1a1"
                    parent_hub = "hub_s3"
                elif "internal/cdc" in file_path:
                    matched_sub = "events"
                    matched_color = "#ff9f43"
                    parent_hub = "hub_cdc"
                elif "internal/workers" in file_path or "internal/derivation" in file_path:
                    matched_sub = "workers"
                    matched_color = "#ff6b6b"
                    parent_hub = "hub_workers"
                elif "web" in file_path:
                    matched_sub = "frontend"
                    matched_color = "#00d2d3"
                    parent_hub = "hub_client"

                node_id = f"file_{file_path}"
                nodes.append({
                    "id": node_id,
                    "name": f,
                    "path": file_path,
                    "category": matched_sub,
                    "color": matched_color,
                    "val": min(max(file_size // 300, 4), 16),
                    "tier": 5,
                    "desc": f"Source File ({file_size} bytes)"
                })

                links.append({
                    "source": parent_hub,
                    "target": node_id,
                    "label": "Contains File",
                    "value": 1
                })

    graph_data = {
        "nodes": nodes,
        "links": links,
        "tours": [
            {
                "step": 1,
                "title": "1. Client & WebAssembly FastCDC",
                "focus_node": "hub_client",
                "desc": "Clients compute FastCDC rolling hashes locally in Wasm/TypeScript. The backend validates chunk manifests to perform global zero-byte deduplication before raw binary uploads."
            },
            {
                "step": 2,
                "title": "2. Stateless Ingress & Auth Engine",
                "focus_node": "hub_ingress",
                "desc": "Go ingress API verifies tenant-scoped HMAC upload nonces, evaluates token bucket rate limits in Redis, and issues presigned direct-to-S3 storage tokens."
            },
            {
                "step": 3,
                "title": "3. High-Performance Rust SIMD Chunking",
                "focus_node": "hub_rust_fastcdc",
                "desc": "Server-side content-defined chunking executes via SIMD-accelerated Rust FFI bindings, achieving >15,000 ops/sec processing speed with zero-copy buffer slicing."
            },
            {
                "step": 4,
                "title": "4. PostgreSQL 16 Metadata & ltree Hierarchy",
                "focus_node": "hub_postgres",
                "desc": "Metadata operations execute in sub-milliseconds using PostgreSQL ltree for O(1) directory tree moves and 16 declarative hash partitions for cas_blocks."
            },
            {
                "step": 5,
                "title": "5. Content-Addressed Object Storage (CAS)",
                "focus_node": "hub_s3",
                "desc": "Physical binary payload bytes stream directly into S3/MinIO object storage under immutable SHA-256 CAS keys, isolated from metadata control plane traffic."
            },
            {
                "step": 6,
                "title": "6. Debezium CDC Event Bus & Workers",
                "focus_node": "hub_cdc",
                "desc": "PostgreSQL WAL commits stream into Debezium CDC and Kafka/NATS channels, triggering asynchronous background workers for ClamAV virus scanning, thumbnails, and indexing."
            }
        ]
    }

    os.makedirs(os.path.dirname(OUTPUT_PATH), exist_ok=True)
    with open(OUTPUT_PATH, "w", encoding="utf-8") as f:
        json.dump(graph_data, f, indent=2)

    print(f"[OK] Generated 3D graph data with {len(nodes)} nodes and {len(links)} links at {OUTPUT_PATH}")

if __name__ == "__main__":
    scan_repo()
