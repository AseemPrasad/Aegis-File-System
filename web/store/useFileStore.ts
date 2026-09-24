import { create } from 'zustand';
import { NamespaceNode, ActiveUploadItem } from '../types/aegis';

interface FileState {
  currentTenantId: string;
  currentFolderId: string | null;
  nodes: NamespaceNode[];
  uploadQueue: ActiveUploadItem[];
  totalDedupSavedBytes: number;
  
  // Actions
  setTenantId: (tenantId: string) => void;
  setFolderId: (folderId: string | null) => void;
  setNodes: (nodes: NamespaceNode[]) => void;
  
  // Optimistic UI updates
  optimisticCreateFolder: (name: string) => string;
  optimisticRenameNode: (nodeId: string, newName: string) => void;
  optimisticDeleteNode: (nodeId: string) => void;
  rollbackNodes: (previousNodes: NamespaceNode[]) => void;
  
  // Upload Queue Actions
  addUploadItem: (item: ActiveUploadItem) => void;
  updateUploadProgress: (id: string, uploadedBytes: number, speed: number) => void;
  setUploadStatus: (id: string, status: ActiveUploadItem['status'], error?: string) => void;
  addDedupSavings: (bytes: number) => void;
  
  // WebSocket Live Updates
  updateNodeStatus: (nodeId: string, status: NamespaceNode['processing_status']) => void;
}

export const useFileStore = create<FileState>((set, get) => ({
  currentTenantId: '00000000-0000-0000-0000-000000000001', // Default dev tenant
  currentFolderId: null,
  nodes: [
    {
      node_id: 'node-root-1',
      tenant_id: '00000000-0000-0000-0000-000000000001',
      parent_id: null,
      name: 'Documents',
      type: 'DIRECTORY',
      lineage_path: 'root.documents',
      size_bytes: 45210000,
      version_id: 'v1.0.0',
      created_at: new Date(Date.now() - 86400000 * 2).toISOString(),
      updated_at: new Date(Date.now() - 86400000 * 2).toISOString(),
    },
    {
      node_id: 'node-file-1',
      tenant_id: '00000000-0000-0000-0000-000000000001',
      parent_id: null,
      name: 'system_architecture_spec.pdf',
      type: 'FILE',
      lineage_path: 'root.system_architecture_spec',
      size_bytes: 12450000,
      version_id: 'v1.0.0',
      created_at: new Date(Date.now() - 3600000 * 4).toISOString(),
      updated_at: new Date(Date.now() - 3600000 * 4).toISOString(),
      processing_status: 'COMMIT_COMPLETE',
    },
  ],
  uploadQueue: [],
  totalDedupSavedBytes: 104857600, // 100MB saved

  setTenantId: (tenantId) => set({ currentTenantId: tenantId }),
  setFolderId: (folderId) => set({ currentFolderId: folderId }),
  setNodes: (nodes) => set({ nodes }),

  optimisticCreateFolder: (name) => {
    const previousNodes = get().nodes;
    const tempId = `temp-${Date.now()}`;
    const newNode: NamespaceNode = {
      node_id: tempId,
      tenant_id: get().currentTenantId,
      parent_id: get().currentFolderId,
      name,
      type: 'DIRECTORY',
      lineage_path: `root.${name.toLowerCase().replace(/\s+/g, '_')}`,
      size_bytes: 0,
      version_id: 'v0.0.1',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };

    set({ nodes: [newNode, ...previousNodes] });
    return tempId;
  },

  optimisticRenameNode: (nodeId, newName) => {
    set((state) => ({
      nodes: state.nodes.map((node) =>
        node.node_id === nodeId ? { ...node, name: newName, updated_at: new Date().toISOString() } : node
      ),
    }));
  },

  optimisticDeleteNode: (nodeId) => {
    set((state) => ({
      nodes: state.nodes.filter((node) => node.node_id !== nodeId),
    }));
  },

  rollbackNodes: (previousNodes) => set({ nodes: previousNodes }),

  addUploadItem: (item) =>
    set((state) => ({
      uploadQueue: [item, ...state.uploadQueue],
    })),

  updateUploadProgress: (id, uploadedBytes, speed) =>
    set((state) => ({
      uploadQueue: state.uploadQueue.map((item) => {
        if (item.id !== id) return item;
        const progress = Math.min(100, Math.round((uploadedBytes / item.totalBytes) * 100));
        return {
          ...item,
          uploadedBytes,
          progress,
          speedBytesPerSec: speed,
        };
      }),
    })),

  setUploadStatus: (id, status, error) =>
    set((state) => ({
      uploadQueue: state.uploadQueue.map((item) =>
        item.id === id ? { ...item, status, error } : item
      ),
    })),

  addDedupSavings: (bytes) =>
    set((state) => ({
      totalDedupSavedBytes: state.totalDedupSavedBytes + bytes,
    })),

  updateNodeStatus: (nodeId, status) =>
    set((state) => ({
      nodes: state.nodes.map((node) =>
        node.node_id === nodeId ? { ...node, processing_status: status } : node
      ),
    })),
}));
