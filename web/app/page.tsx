'use client';

import React, { useEffect } from 'react';
import { TreeNav } from '@/components/tree-nav';
import { DataTable } from '@/components/data-table';
import { UploadMonitor } from '@/components/upload-monitor';
import { useFileStore } from '@/store/useFileStore';
import { AegisUploadEngine } from '@/lib/upload/upload-engine';
import { aegisWS } from '@/lib/socket/ws-client';
import { Plus, Upload, ShieldCheck, Activity } from 'lucide-react';

export default function WorkspaceConsolePage() {
  const {
    currentTenantId,
    currentFolderId,
    optimisticCreateFolder,
    addUploadItem,
    updateUploadProgress,
    setUploadStatus,
    addDedupSavings,
    updateNodeStatus,
  } = useFileStore();

  const fileInputRef = React.useRef<HTMLInputElement | null>(null);

  // Initialize WebSocket connection for live event streaming
  useEffect(() => {
    aegisWS.connect();
    const unsubscribe = aegisWS.subscribe((event) => {
      if (event.event_type === 'FILE_STATUS_UPDATE' && event.node_id) {
        updateNodeStatus(
          event.node_id,
          event.payload.status as 'CLAMAV_SCANNING' | 'OCR_PROCESSING' | 'THUMBNAIL_READY' | 'COMMIT_COMPLETE'
        );
      }
    });

    return () => {
      unsubscribe();
      aegisWS.disconnect();
    };
  }, [updateNodeStatus]);

  const handleFileUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files || files.length === 0) return;

    const uploadEngine = new AegisUploadEngine();

    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      const uploadId = `up-${Date.now()}-${i}`;

      addUploadItem({
        id: uploadId,
        fileName: file.name,
        totalBytes: file.size,
        uploadedBytes: 0,
        dedupBytesSaved: 0,
        progress: 0,
        speedBytesPerSec: 0,
        status: 'HASHING',
      });

      try {
        await uploadEngine.uploadFile(file, currentTenantId, currentFolderId, {
          onProgress: (uploadedBytes, speed) => {
            updateUploadProgress(uploadId, uploadedBytes, speed);
          },
          onDedupFound: (savedBytes) => {
            addDedupSavings(savedBytes);
          },
          onStatusChange: (status, error) => {
            setUploadStatus(uploadId, status, error);
          },
        });
      } catch (err) {
        console.error('Upload failed:', err);
      }
    }
  };

  return (
    <div className="flex h-screen w-screen bg-slate-950 text-slate-100 font-sans antialiased overflow-hidden selection:bg-indigo-500 selection:text-white">
      {/* Sidebar Navigation */}
      <TreeNav />

      {/* Main Workspace Body */}
      <main className="flex-1 flex flex-col min-w-0 overflow-hidden bg-gradient-to-b from-slate-950 via-slate-900 to-slate-950">
        {/* Top Header Controls */}
        <header className="h-16 px-8 border-b border-slate-800/80 flex items-center justify-between bg-slate-950/40 backdrop-blur-sm shrink-0">
          <div className="flex items-center gap-3">
            <div className="p-1.5 rounded-md bg-indigo-500/10 text-indigo-400 border border-indigo-500/20">
              <Activity className="w-4 h-4" />
            </div>
            <h2 className="text-sm font-semibold text-slate-200">Tenant Filesystem Graph</h2>
            <span className="text-xs font-mono text-slate-500">/ root</span>
          </div>

          <div className="flex items-center gap-3">
            <input
              type="file"
              ref={fileInputRef}
              onChange={handleFileUpload}
              className="hidden"
              multiple
            />
            <button
              onClick={() => {
                const folderName = prompt('Enter new directory name:');
                if (folderName) optimisticCreateFolder(folderName);
              }}
              className="flex items-center gap-2 px-3.5 py-1.5 rounded-lg border border-slate-700 bg-slate-900 hover:bg-slate-800 text-slate-200 text-xs font-semibold transition-all shadow-sm"
            >
              <Plus className="w-4 h-4 text-indigo-400" /> New Folder
            </button>
            <button
              onClick={() => fileInputRef.current?.click()}
              className="flex items-center gap-2 px-4 py-1.5 rounded-lg bg-gradient-to-r from-indigo-500 to-sky-500 hover:from-indigo-600 hover:to-sky-600 text-white text-xs font-semibold transition-all shadow-lg shadow-indigo-500/20"
            >
              <Upload className="w-4 h-4" /> Upload Stream
            </button>
          </div>
        </header>

        {/* Content Area */}
        <div className="flex-1 p-8 overflow-y-auto space-y-6">
          {/* Upload Progress Monitor Banner */}
          <UploadMonitor />

          {/* Main Data Table */}
          <DataTable />
        </div>

        {/* Status Bar */}
        <footer className="h-9 px-8 border-t border-slate-800/80 bg-slate-950/80 flex items-center justify-between text-[11px] text-slate-500 font-mono shrink-0">
          <div className="flex items-center gap-4">
            <span className="flex items-center gap-1.5 text-emerald-400">
              <ShieldCheck className="w-3.5 h-3.5" /> Bit-Perfect CAS Invariant Verified
            </span>
            <span>FastCDC Gear Hashing Active</span>
          </div>
          <div>Project Aegis v1.0.0-enterprise</div>
        </footer>
      </main>
    </div>
  );
}
