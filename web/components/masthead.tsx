"use client";

import { Settings, X } from "lucide-react";

import type { Status } from "../lib/sync.ts";

const LABEL: Record<Status, string> = { online: "Connected", connecting: "Connecting…", offline: "Offline, retrying" };

export function Masthead({ status, devices, settingsOpen, onSettings }: { status?: Status; devices?: number; settingsOpen?: boolean; onSettings?: () => void }) {
  return (
    <header className="masthead">
      <h1 className="wordmark">ClipSync</h1>
      {status ? (
        <span className="status" data-state={status} role="status">
          <span className="status-dot" aria-hidden />
          {LABEL[status]}
          {status === "online" && devices !== undefined && <span>· {devices === 1 ? "1 other device" : `${devices} other devices`}</span>}
        </span>
      ) : (
        <span className="status" />
      )}
      {onSettings && (
        <button type="button" className="icon-btn" onClick={onSettings} aria-label={settingsOpen ? "Close settings" : "Settings"} aria-expanded={settingsOpen}>
          {settingsOpen ? <X size={22} strokeWidth={1.75} /> : <Settings size={22} strokeWidth={1.75} />}
        </button>
      )}
    </header>
  );
}
