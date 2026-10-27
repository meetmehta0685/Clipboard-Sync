// Persistent phone state in localStorage. The identity key never leaves the
// device; the group key arrives sealed to it during pairing.
import { GroupKey, identityFrom, newIdentity, uuid, type Identity } from "./crypto.ts";
import { b64decode, b64encode, decode, encode, type Device } from "./protocol.ts";

const KEY = "clipsync:v1";

export interface Saved {
  deviceId: string;
  name: string;
  priv: string; // base64 X25519 private key
  // "secret", not "key": decode() turns protocol fields named key into bytes.
  group?: { secret: string; epoch: number };
  devices: Device[];
  gen: string;
  seq: number;
  clock: number;
}

export interface StoredClip {
  id: string;
  origin: string;
  type: "text" | "image" | "file";
  size: number;
  clock: number;
  created: number;
  local: boolean;
  text?: string; // text clips, or the file name for files
}

const isBrowser = () => typeof window !== "undefined" && typeof localStorage !== "undefined";

function defaultName(): string {
  if (!isBrowser()) return "Phone";
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return "iPhone";
  if (/iPad/.test(ua)) return "iPad";
  if (/Android/.test(ua)) return "Android phone";
  return "Browser";
}

export function load(): Saved {
  if (isBrowser()) {
    const raw = localStorage.getItem(KEY);
    if (raw) return decode<Saved>(raw);
  }
  const id = newIdentity();
  const s: Saved = { deviceId: uuid(), name: defaultName(), priv: b64encode(id.priv), devices: [], gen: "", seq: 0, clock: 0 };
  save(s);
  return s;
}

/**
 * Writes state, merging with what another tab may have stored meanwhile
 * (the share sheet can open a second window beside the installed app).
 * The Lamport clock and relay cursor only move forward, and a newer group
 * key epoch wins, so a stale tab can never roll them back.
 */
export function save(s: Saved) {
  if (!isBrowser()) return;
  const raw = localStorage.getItem(KEY);
  if (raw) {
    const cur = decode<Saved>(raw);
    if (cur.deviceId === s.deviceId) {
      s.clock = Math.max(s.clock, cur.clock);
      if (cur.group?.secret && (!s.group || cur.group.epoch > s.group.epoch)) {
        s.group = cur.group;
        s.devices = cur.devices;
        s.gen = cur.gen;
        s.seq = cur.seq;
      } else if (cur.gen === s.gen) {
        s.seq = Math.max(s.seq, cur.seq);
      }
      for (const d of cur.devices) if (!s.devices.some((x) => x.id === d.id)) s.devices.push(d);
    }
  }
  localStorage.setItem(KEY, encode(s));
}

export function identity(s: Saved): Identity {
  return identityFrom(b64decode(s.priv));
}

export function groupKey(s: Saved): GroupKey | null {
  return s.group?.secret ? new GroupKey(b64decode(s.group.secret), s.group.epoch) : null;
}

export function setGroup(s: Saved, key: Uint8Array, epoch: number, devices: Device[]) {
  s.group = { secret: b64encode(key), epoch };
  s.devices = devices;
  s.gen = "";
  s.seq = 0; // new key, new relay room
  // Bypass the merge: a new group legitimately resets the cursor.
  if (isBrowser()) localStorage.setItem(KEY, encode({ ...s, clock: Math.max(s.clock, load().clock) }));
}

export function forget() {
  if (isBrowser()) {
    localStorage.removeItem(KEY);
    localStorage.removeItem(HISTORY);
  }
}

const HISTORY = "clipsync:history";
const HISTORY_MAX = 100;

export function loadHistory(): StoredClip[] {
  if (!isBrowser()) return [];
  try {
    return JSON.parse(localStorage.getItem(HISTORY) ?? "[]");
  } catch {
    return [];
  }
}

export function saveHistory(clips: StoredClip[]) {
  if (isBrowser()) localStorage.setItem(HISTORY, JSON.stringify(clips.slice(0, HISTORY_MAX)));
}

/** Same-origin relay URL: the relay serves this app, so /ws is next door. */
export function relayUrl(): string {
  const override = process.env.NEXT_PUBLIC_RELAY_URL;
  if (override) return override;
  if (!isBrowser()) return "";
  return `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`;
}
