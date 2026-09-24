'use client';

import React from 'react';
import { useFileStore } from '../store/useFileStore';
import { Folder, HardDrive, Shield, Activity, Settings, Database } from 'lucide-react';

export const TreeNav: React.FC = () => {
  const { currentTenantId, totalDedupSavedBytes } = useFileStore();

  return (
    <aside className="w-64 border-r border-slate-800 bg-slate-950/80 p-5 flex flex-col justify-between h-full backdrop-blur-md">
      <div className="space-y-6">
        {/* Workspace Header */}
        <div className="flex items-center gap-3 px-2">
          <div className="p-2 rounded-xl bg-gradient-to-tr from-indigo-500 to-sky-400 text-slate-950 font-black shadow-lg shadow-indigo-500/20">
            <Shield className="w-5 h-5 fill-slate-950" />
          </div>
          <div>
            <h1 className="text-base font-bold text-slate-100 tracking-tight">AEGIS</h1>
            <p className="text-[10px] font-mono text-indigo-400 uppercase tracking-wider">CAS Storage Engine</p>
          </div>
        </div>

        {/* Navigation Sections */}
        <nav className="space-y-1">
          <div className="px-2 pb-2 text-[10px] font-semibold text-slate-500 uppercase tracking-wider font-mono">
            Tenant Navigation
          </div>
          <a
            href="#"
            className="flex items-center gap-3 px-3 py-2 rounded-lg bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 text-xs font-semibold"
          >
            <Folder className="w-4 h-4" /> Workspace Files
          </a>
          <a
            href="#"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-400 hover:bg-slate-900 hover:text-slate-200 text-xs font-medium transition-colors"
          >
            <Activity className="w-4 h-4" /> CAS Analytics
          </a>
          <a
            href="#"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-400 hover:bg-slate-900 hover:text-slate-200 text-xs font-medium transition-colors"
          >
            <Database className="w-4 h-4" /> Block Registry
          </a>
          <a
            href="#"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-400 hover:bg-slate-900 hover:text-slate-200 text-xs font-medium transition-colors"
          >
            <Settings className="w-4 h-4" /> KMS & Security
          </a>
        </nav>
      </div>

      {/* Footer Tenant Status */}
      <div className="p-3.5 rounded-xl bg-slate-900/60 border border-slate-800/80 space-y-2 text-xs">
        <div className="flex items-center justify-between text-slate-400 font-mono text-[10px]">
          <span>Tenant Scope</span>
          <span className="text-emerald-400 flex items-center gap-1 font-semibold">● Active</span>
        </div>
        <div className="font-mono text-[11px] text-slate-200 truncate">{currentTenantId}</div>
        <div className="pt-2 border-t border-slate-800/80 flex items-center gap-2 text-[11px] text-slate-400">
          <HardDrive className="w-3.5 h-3.5 text-indigo-400" />
          <span>Saved: <strong className="text-slate-200">{(totalDedupSavedBytes / (1024 * 1024)).toFixed(0)} MB</strong></span>
        </div>
      </div>
    </aside>
  );
};
