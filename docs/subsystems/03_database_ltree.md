# Subsystem Spec 03: PostgreSQL Schema, `ltree` Graph & Partitioning

## 1. Relational Schema Architecture (`db/schema.sql`)

PostgreSQL 16 serves as the primary metadata engine for tenants, namespace nodes, version histories, and CAS block tracking.

```
┌─────────────┐       1:N       ┌─────────────────┐       1:N       ┌──────────────────┐
│   tenants   ├────────────────►│ namespace_nodes ├────────────────►│  file_versions   │
└──────┬──────┘                 └─────────────────┘                 └────────┬─────────┘
       │                                                                     │ 1:N
       │ 1:N                                                                 ▼
       │                        ┌─────────────────┐       1:N       ┌──────────────────┐
       └───────────────────────►│   cas_blocks    │◄────────────────┤manifest_blocks   │
                                └─────────────────┘                 └──────────────────┘
```

---

## 2. Acyclic Namespace Graph (`ltree`)

Directory structures are modeled using PostgreSQL's `ltree` extension (`lineage_path` column).

### Node Path Format:
- **Tenant Root:** `'root_9f8b7c6d'`
- **Folder Node:** `'root_9f8b7c6d.folder_1a2b3c4d'`
- **File Node:** `'root_9f8b7c6d.folder_1a2b3c4d.file_5e6f7a8b'`

### Subtree Ancestor Resolution Query (O(1) Indexed Walk):
```sql
SELECT node_id, name, lineage_path 
FROM namespace_nodes 
WHERE lineage_path <@ 'root_9f8b7c6d.folder_1a2b3c4d'::ltree 
  AND is_deleted = FALSE;
```

---

## 3. Declarative Hash Partitioning (`cas_blocks_partitioned`)

High-volume `cas_blocks` tables are partitioned into 16 declarative hash slices (`cas_blocks_p0` through `cas_blocks_p15`) based on `block_hash` value (`PARTITION BY HASH (block_hash)`).

---

## 4. Transaction Lock Fencing (`CommitFile`)

To prevent version number race conditions on concurrent file uploads, `CommitFile()` locks `namespace_nodes` (`FOR UPDATE`) before evaluating `SELECT next_version_number(node_id)`. This guarantees dense, strictly increasing version numbers (`v1`, `v2`, `v3`) without version collisions.
