'use client';

import React from 'react';
import { TreeNav } from '@/components/tree-nav';
import { Activity, Zap, HardDrive, BarChart3 } from 'lucide-react';
import { useFileStore } from '@/store/useFileStore';

export default function TenantAnalyticsPage() {
  const { totalDedupSavedBytes } = useFileStore();

  return (
    <div className="flex h-screen w-screen bg-slate-950 text-slate-100 font-sans antialiased overflow-hidden">
      <TreeNav />
      <main className="flex-1 flex flex-col min-w-0 overflow-hidden bg-gradient-to-b from-slate-950 via-slate-900 to-slate-950">
        <header className="h-16 px-8 border-b border-slate-800/80 flex items-center gap-3 bg-slate-950/40 backdrop-blur-sm shrink-0">
          <Activity className="w-5 h-5 text-indigo-400" />
          <h2 className="text-sm font-semibold text-slate-200">CAS Deduplication Analytics & Storage Metrics</h2>
        </header>

        <div className="flex-1 p-8 overflow-y-auto space-y-6">
          <div className="grid grid-cols-3 gap-6">
            <div className="p-6 rounded-xl border border-slate-800 bg-slate-900/60 backdrop-blur-md space-y-2">
              <div className="flex items-center justify-between text-xs text-slate-400">
                <span>CAS Deduplication Savings</span>
                <Zap className="w-4 h-4 text-emerald-400" />
              </div>
              <div className="text-2xl font-bold font-mono text-emerald-400">
                {(totalDedupSavedBytes / (1024 * 1024)).toFixed(1)} MB
              </div>
              <p className="text-xs text-slate-500">Global payload compression ratio: 38.4%</p>
            </div>

            <div className="p-6 rounded-xl border border-slate-800 bg-slate-900/60 backdrop-blur-md space-y-2">
              <div className="flex items-center justify-between text-xs text-slate-400">
                <span>FastCDC Chunk Count</span>
                <HardDrive className="w-4 h-4 text-indigo-400" />
              </div>
              <div className="text-2xl font-bold font-mono text-slate-100">1,482 Chunks</div>
              <p className="text-xs text-slate-500">Average chunk size: 3.8 MB</p>
            </div>

            <div className="p-6 rounded-xl border border-slate-800 bg-slate-900/60 backdrop-blur-md space-y-2">
              <div className="flex items-center justify-between text-xs text-slate-400">
                <span>Ingest Throughput</span>
                <BarChart3 className="w-4 h-4 text-sky-400" />
              </div>
              <div className="text-2xl font-bold font-mono text-sky-400">2.4 GB/s</div>
              <p className="text-xs text-slate-500">Rust SIMD FastCDC algorithm target</p>
            </div>
          </div>
        </div>
      </main>
    </div>
  );
}
