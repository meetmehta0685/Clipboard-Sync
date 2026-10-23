"use client";

import { useEffect, useReducer, useState } from "react";

import { load, type Saved } from "../lib/state.ts";
import { SyncClient } from "../lib/sync.ts";

let shared: SyncClient | null = null;

/** One SyncClient per page, started once the phone is paired. */
export function useSync() {
  const [saved, setSaved] = useState<Saved | null>(null);
  const [client, setClient] = useState<SyncClient | null>(null);
  const [, rerender] = useReducer((n: number) => n + 1, 0);

  useEffect(() => {
    const s = load();
    setSaved(s);
    if (!s.group) return;
    if (!shared) {
      shared = new SyncClient(s);
      shared.start();
    }
    const c = shared;
    setClient(c);
    c.addEventListener("change", rerender);
    return () => c.removeEventListener("change", rerender);
  }, []);

  return { saved, client };
}

export function resetShared() {
  shared?.stop();
  shared = null;
}

export const clipboardAvailable = () =>
  typeof navigator !== "undefined" && !!navigator.clipboard && typeof window !== "undefined" && window.isSecureContext;
