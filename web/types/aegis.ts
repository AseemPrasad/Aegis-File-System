export type NodeType = 'FILE' | 'DIRECTORY';

export interface NamespaceNode {
  node_id: string;
  tenant_id: string;
  parent_id: string | null;
  name: string;
  type: NodeType;
  lineage_path: string; // Postgres ltree format e.g. "root.folder1.file"
  size_bytes: number;
  version_id: string;
  created_at: string;
  updated_at: string;
  processing_status?: 'IDLE' | 'CLAMAV_SCANNING' | 'OCR_PROCESSING' | 'THUMBNAIL_READY' | 'COMMIT_COMPLETE';
}

export interface ManifestBlock {
  block_hash: string;
  offset_bytes: number;
  size_bytes: number;
  chunk_index?: number;
}

export interface InitiateUploadRequest {
  tenant_id: string;
  file_name: string;
  parent_id?: string | null;
  total_size?: number;
  total_bytes?: number;
  chunks?: {
    block_hash: string;
    size_bytes: number;
  }[];
  blocks?: {
    block_hash: string;
    size_bytes: number;
  }[];
}

export interface InitiateUploadResponse {
  session_id: string;
  node_id?: string;
  expires_at?: string;
  missing_blocks?: {
    block_hash: string;
    upload_url: string;
  }[];
  upload_urls?: {
    block_hash: string;
    url: string;
    size_bytes: number;
  }[];
  existing_block_count?: number;
  dedup_bytes_saved?: number;
}

export interface CommitUploadRequest {
  session_id: string;
  tenant_id?: string;
  file_name?: string;
  parent_id?: string | null;
  content_sha256?: string;
  blocks: ManifestBlock[];
}

export interface CommitUploadResponse {
  node_id?: string;
  version_id: string;
  version_number?: number;
  status?: 'COMMITTED';
}

export interface ActiveUploadItem {
  id: string;
  fileName: string;
  totalBytes: number;
  uploadedBytes: number;
  dedupBytesSaved: number;
  progress: number;
  speedBytesPerSec: number;
  status: 'HASHING' | 'INITIATING' | 'UPLOADING' | 'COMMITTING' | 'COMPLETED' | 'FAILED';
  error?: string;
}

export interface WSEventMessage {
  event_type: 'FILE_STATUS_UPDATE' | 'DEDUP_METRIC_UPDATE' | 'QUOTA_EXCEEDED';
  tenant_id: string;
  node_id?: string;
  payload: Record<string, unknown>;
  timestamp: string;
}
