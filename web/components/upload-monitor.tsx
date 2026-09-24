'use client';

import React from 'react';
import { useFileStore } from '../store/useFileStore';
import { HardDrive, Zap, RefreshCw, UploadCloud, Layers } from 'lucide-react';

export const UploadMonitor: React.FC = () => {
  const { uploadQueue, totalDedupSavedBytes } = useFileStore();

  const formatMB = (bytes: number) => (bytes / (1024 * 1024)).toFixed(1);

  if (uploadQueue.length === 0) {
    return (
      <div className="p-6 rounded-xl border border-slate-800 bg-slate-900/40 backdrop-blur-md flex items-center justify-between">
        <div className="flex items-center gap-4">
          <div className="p-3 rounded-lg bg-indigo-500/10 border border-indigo-500/20 text-indigo-400">
            <UploadCloud className="w-6 h-6" />
          </div>
          <div>
            <h3 className="text-sm font-semibold text-slate-200">FastCDC Stream Ingestion Ready</h3>
            <p className="text-xs text-slate-400">Select files to slice dynamic 2MB-8MB content-defined chunks</p>
          </div>
        </div>
        <div className="flex items-center gap-6">
          <div className="text-right">
            <div className="text-xs text-slate-500 font-medium">CAS Global Deduplication</div>
            <div className="text-sm font-bold text-emerald-400 flex items-center gap-1">
              <Zap className="w-4 h-4 fill-emerald-400" /> {formatMB(totalDedupSavedBytes)} MB Saved
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="p-6 rounded-xl border border-slate-800 bg-slate-900/60 backdrop-blur-md shadow-xl space-y-4">
      <div className="flex items-center justify-between border-b border-slate-800 pb-4">
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-lg bg-indigo-500/10 text-indigo-400 border border-indigo-500/20">
            <Layers className="w-5 h-5 animate-pulse" />
          </div>
          <div>
            <h3 className="text-sm font-semibold text-slate-100">Live FastCDC Chunk Upload Queue</h3>
            <p className="text-xs text-slate-400">Parallel HMAC pre-signed S3 stream ingestion</p>
          </div>
        </div>
        <div className="flex items-center gap-4 text-xs font-mono">
          <div className="px-3 py-1.5 rounded-lg bg-slate-950 border border-slate-800 text-slate-300 flex items-center gap-2">
            <HardDrive className="w-3.5 h-3.5 text-indigo-400" />
            <span>Dedup Saved: <strong className="text-emerald-400">{formatMB(totalDedupSavedBytes)} MB</strong></span>
          </div>
        </div>
      </div>

      <div className="space-y-3 max-h-60 overflow-y-auto pr-1">
        {uploadQueue.map((item) => (
          <div
            key={item.id}
            className="p-3.5 rounded-lg bg-slate-950/70 border border-slate-800/80 flex items-center justify-between gap-4 text-xs"
          >
            <div className="min-w-0 flex-1 space-y-1.5">
              <div className="flex items-center justify-between">
                <span className="font-medium text-slate-200 truncate">{item.fileName}</span>
                <span className="font-mono text-slate-400">{item.progress}%</span>
              </div>
              <div className="w-full bg-slate-800 rounded-full h-1.5 overflow-hidden">
                <div
                  className="bg-gradient-to-r from-indigo-500 to-sky-400 h-1.5 rounded-full transition-all duration-300"
                  style={{ width: `${item.progress}%` }}
                />
              </div>
            </div>

            <div className="flex items-center gap-4 text-right font-mono text-slate-400">
              <div>
                <div>{formatMB(item.uploadedBytes)} / {formatMB(item.totalBytes)} MB</div>
                <div className="text-[10px] text-slate-500">
                  {item.speedBytesPerSec > 0
                    ? `${(item.speedBytesPerSec / (1024 * 1024)).toFixed(1)} MB/s`
                    : item.status}
                </div>
              </div>
              {item.status === 'UPLOADING' && <RefreshCw className="w-4 h-4 text-indigo-400 animate-spin" />}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
};
