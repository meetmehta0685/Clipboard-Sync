// Wire types, mirroring internal/protocol. Go encodes []byte as standard
// base64 strings; encode/decode convert those fields to Uint8Array.

export interface Envelope {
  v: number;
  id: string;
  origin: string;
  clock: number;
  epoch: number;
  type: "text" | "image" | "file";
  hash: Uint8Array;
  size: number;
  chunks: number;
  sent: number;
  nonce: Uint8Array;
  payload: Uint8Array;
}

export interface Chunk {
  id: string;
  index: number;
  nonce: Uint8Array;
  payload: Uint8Array;
}

export interface Need {
  id: string;
  indexes: number[];
  from: string;
}

export interface Hello {
  v: number;
  room: string;
  token?: Uint8Array;
  device: string;
  gen?: string;
  since: number;
  pair?: boolean;
}

export interface Control {
  origin: string;
  clock: number;
  epoch: number;
  nonce: Uint8Array;
  payload: Uint8Array;
}

export interface Sealed {
  eph: Uint8Array;
  sender: Uint8Array;
  nonce: Uint8Array;
  payload: Uint8Array;
}

export interface Rekey {
  to: string;
  from: string;
  epoch: number;
  clock: number;
  sealed: Sealed;
}

export interface PairHello {
  device: string;
  name: string;
  kind: string;
  pub: Uint8Array;
  mac: Uint8Array;
}

export interface PairWelcome {
  sealed: Sealed;
  mac: Uint8Array;
}

export interface Device {
  id: string;
  name: string;
  pub: Uint8Array;
  kind: string;
  added: string;
}

export interface Message {
  kind: "hello" | "ready" | "clip" | "chunk" | "need" | "control" | "rekey" | "error" | "pair_hello" | "pair_welcome";
  clip?: Envelope;
  chunk?: Chunk;
  need?: Need;
  hello?: Hello;
  control?: Control;
  rekey?: Rekey;
  pair_hello?: PairHello;
  pair_welcome?: PairWelcome;
  error?: string;
  seq?: number;
  gen?: string;
}

// Field names that hold bytes anywhere in the protocol or its sealed payloads.
const BYTE_FIELDS = new Set(["hash", "nonce", "payload", "token", "eph", "sender", "pub", "mac", "key"]);

export function b64encode(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
  return btoa(s);
}

export function b64decode(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function b64urlEncode(b: Uint8Array): string {
  return b64encode(b).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function b64urlDecode(s: string): Uint8Array {
  const pad = s.length % 4 === 0 ? "" : "=".repeat(4 - (s.length % 4));
  return b64decode(s.replace(/-/g, "+").replace(/_/g, "/") + pad);
}

/** JSON.stringify with Uint8Array → base64. */
export function encode(v: unknown): string {
  return JSON.stringify(v, (_k, val) => (val instanceof Uint8Array ? b64encode(val) : val));
}

/** JSON.parse with base64 → Uint8Array for byte fields. */
export function decode<T = Message>(s: string): T {
  return JSON.parse(s, (k, val) => {
    if (BYTE_FIELDS.has(k)) {
      if (typeof val === "string") return b64decode(val);
      if (val === null) return new Uint8Array(0);
    }
    return val;
  });
}
