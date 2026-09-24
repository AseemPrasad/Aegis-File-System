'use client';

import React from 'react';
import { TreeNav } from '@/components/tree-nav';
import { DataTable } from '@/components/data-table';
import { UploadMonitor } from '@/components/upload-monitor';
import { useFileStore } from '@/store/useFileStore';

export default function DynamicTenantFilesPage({ params }: { params: { tenantId: string } }) {
  const { setTenantId } = useFileStore();

  React.useEffect(() => {
    if (params.tenantId) {
      setTenantId(params.tenantId);
    }
  }, [params.tenantId, setTenantId]);

  return (
    <div className="flex h-screen w-screen bg-slate-950 text-slate-100 font-sans antialiased overflow-hidden">
      <TreeNav />
      <main className="flex-1 flex flex-col min-w-0 overflow-hidden bg-gradient-to-b from-slate-950 via-slate-900 to-slate-950">
        <header className="h-16 px-8 border-b border-slate-800/80 flex items-center justify-between bg-slate-950/40 backdrop-blur-sm shrink-0">
          <div>
            <h2 className="text-sm font-semibold text-slate-200">Tenant Workspace</h2>
            <p className="text-xs font-mono text-indigo-400">ID: {params.tenantId}</p>
          </div>
        </header>
        <div className="flex-1 p-8 overflow-y-auto space-y-6">
          <UploadMonitor />
          <DataTable />
        </div>
      </main>
    </div>
  );
}
