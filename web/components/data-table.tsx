'use client';

import React from 'react';
import { useFileStore } from '../store/useFileStore';
import { Folder, FileText, CheckCircle2, Loader2, ShieldCheck, Cpu } from 'lucide-react';

interface DataTableProps {
  onRowClick?: (nodeId: string) => void;
}

export const DataTable: React.FC<DataTableProps> = () => {
  const { nodes, optimisticRenameNode, optimisticDeleteNode } = useFileStore();

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
  };

  const renderStatusBadge = (status?: string) => {
    switch (status) {
      case 'CLAMAV_SCANNING':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium bg-amber-500/10 text-amber-400 border border-amber-500/20">
            <Loader2 className="w-3 h-3 animate-spin" /> ClamAV Scan
          </span>
        );
      case 'OCR_PROCESSING':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium bg-blue-500/10 text-blue-400 border border-blue-500/20">
            <Cpu className="w-3 h-3 animate-pulse" /> OCR Extraction
          </span>
        );
      case 'COMMIT_COMPLETE':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
            <ShieldCheck className="w-3 h-3" /> Bit-Perfect CAS
          </span>
        );
      default:
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium bg-slate-500/10 text-slate-400 border border-slate-500/20">
            <CheckCircle2 className="w-3 h-3" /> Ready
          </span>
        );
    }
  };

  return (
    <div className="w-full overflow-hidden rounded-xl border border-slate-800 bg-slate-900/60 backdrop-blur-md shadow-2xl">
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm text-slate-300">
          <thead className="bg-slate-950/80 uppercase text-xs text-slate-400 font-semibold tracking-wider border-b border-slate-800">
            <tr>
              <th className="px-6 py-4">Name</th>
              <th className="px-6 py-4">DAG Lineage Path</th>
              <th className="px-6 py-4">Size</th>
              <th className="px-6 py-4">Verification Status</th>
              <th className="px-6 py-4 text-right">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800/60">
            {nodes.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-6 py-12 text-center text-slate-500">
                  No files or directories found in this tenant workspace.
                </td>
              </tr>
            ) : (
              nodes.map((node) => (
                <tr
                  key={node.node_id}
                  className="hover:bg-slate-800/40 transition-colors group"
                >
                  <td className="px-6 py-4 font-medium text-slate-100 flex items-center gap-3">
                    {node.type === 'DIRECTORY' ? (
                      <Folder className="w-5 h-5 text-indigo-400 shrink-0" />
                    ) : (
                      <FileText className="w-5 h-5 text-sky-400 shrink-0" />
                    )}
                    <span className="truncate max-w-xs">{node.name}</span>
                  </td>
                  <td className="px-6 py-4 font-mono text-xs text-slate-400">
                    <span className="bg-slate-950/60 px-2 py-1 rounded border border-slate-800">
                      {node.lineage_path}
                    </span>
                  </td>
                  <td className="px-6 py-4 font-mono text-slate-300">
                    {node.type === 'DIRECTORY' ? '-' : formatBytes(node.size_bytes)}
                  </td>
                  <td className="px-6 py-4">
                    {renderStatusBadge(node.processing_status)}
                  </td>
                  <td className="px-6 py-4 text-right space-x-3">
                    <button
                      onClick={() => {
                        const newName = prompt('Enter new node name:', node.name);
                        if (newName && newName !== node.name) {
                          optimisticRenameNode(node.node_id, newName);
                        }
                      }}
                      className="text-xs font-medium text-indigo-400 hover:text-indigo-300 opacity-0 group-hover:opacity-100 transition-opacity"
                    >
                      Rename
                    </button>
                    <button
                      onClick={() => optimisticDeleteNode(node.node_id)}
                      className="text-xs font-medium text-rose-400 hover:text-rose-300 opacity-0 group-hover:opacity-100 transition-opacity"
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
};
