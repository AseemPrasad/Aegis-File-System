'use client';

import React from 'react';
import { TreeNav } from '@/components/tree-nav';
import { Settings, Key, ShieldAlert } from 'lucide-react';

export default function TenantSettingsPage({ params }: { params: { tenantId: string } }) {
  return (
    <div className="flex h-screen w-screen bg-slate-950 text-slate-100 font-sans antialiased overflow-hidden">
      <TreeNav />
      <main className="flex-1 flex flex-col min-w-0 overflow-hidden bg-gradient-to-b from-slate-950 via-slate-900 to-slate-950">
        <header className="h-16 px-8 border-b border-slate-800/80 flex items-center gap-3 bg-slate-950/40 backdrop-blur-sm shrink-0">
          <Settings className="w-5 h-5 text-indigo-400" />
          <h2 className="text-sm font-semibold text-slate-200">Tenant Security & KMS Key Rotation</h2>
        </header>

        <div className="flex-1 p-8 overflow-y-auto space-y-6 max-w-3xl">
          <div className="p-6 rounded-xl border border-slate-800 bg-slate-900/60 backdrop-blur-md space-y-4">
            <h3 className="text-sm font-semibold text-slate-200 flex items-center gap-2">
              <Key className="w-4 h-4 text-indigo-400" /> KMS Envelope Encryption Key
            </h3>
            <div className="font-mono text-xs bg-slate-950 p-3 rounded-lg border border-slate-800 text-slate-300">
              arn:aws:kms:us-east-1:123456789012:key/aegis-tenant-key-001
            </div>
            <div className="flex items-center justify-between text-xs pt-2">
              <span className="text-slate-400">Current Key Version: <strong className="text-slate-200 font-mono">v1</strong></span>
              <button className="px-3 py-1.5 rounded-lg bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 hover:bg-indigo-500/20 font-medium transition-colors">
                Rotate Key Version
              </button>
            </div>
          </div>

          <div className="p-6 rounded-xl border border-rose-500/20 bg-rose-500/5 backdrop-blur-md space-y-3">
            <h3 className="text-sm font-semibold text-rose-400 flex items-center gap-2">
              <ShieldAlert className="w-4 h-4" /> Cryptographic Ingress Token Expiry
            </h3>
            <p className="text-xs text-slate-400">
              Pre-signed HMAC upload URLs are minted with short-lived 900-second validity and single-use Redis replay nonces.
            </p>
          </div>
        </div>
      </main>
    </div>
  );
}
