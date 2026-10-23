// Port of internal/crypto. Every construction here must match the Go code
// byte for byte; test/vectors.test.ts checks this against Go-generated
// vectors and writes vectors in the other direction for the Go tests.
//
// WebCrypto has X25519, HKDF and HMAC but no XChaCha20-Poly1305, so we use
// the audited @noble libraries for everything and keep one code path.
import { xchacha20poly1305 } from "@noble/ciphers/chacha.js";
import { x25519 } from "@noble/curves/ed25519.js";
import { expand, extract, hkdf } from "@noble/hashes/hkdf.js";
import { hmac } from "@noble/hashes/hmac.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { bytesToHex, concatBytes, randomBytes, utf8ToBytes } from "@noble/hashes/utils.js";

import type { Control, Envelope, Sealed, Chunk } from "./protocol.ts";

export const KEY_SIZE = 32;
export const CHUNK_SIZE = 256 * 1024;

export { randomBytes, bytesToHex, utf8ToBytes, concatBytes };

const derive = (secret: Uint8Array, info: string, n: number) =>
  hkdf(sha256, secret, undefined, utf8ToBytes(info), n);

export interface Identity {
  priv: Uint8Array;
  pub: Uint8Array;
}

export function newIdentity(): Identity {
  const priv = randomBytes(32);
  return { priv, pub: x25519.getPublicKey(priv) };
}

export function identityFrom(priv: Uint8Array): Identity {
  return { priv, pub: x25519.getPublicKey(priv) };
}

export class GroupKey {
  readonly key: Uint8Array;
  readonly epoch: number;
  readonly contentKey: Uint8Array;
  readonly hashKey: Uint8Array;

  constructor(key: Uint8Array, epoch: number) {
    this.key = key;
    this.epoch = epoch;
    if (key.length !== KEY_SIZE) throw new Error("group key must be 32 bytes");
    this.contentKey = derive(key, "clipsync content key", KEY_SIZE);
    this.hashKey = derive(key, "clipsync hash key", KEY_SIZE);
  }

  roomId(): string {
    return bytesToHex(derive(this.key, "clipsync room id", 16));
  }

  roomToken(): Uint8Array {
    return derive(this.key, "clipsync room token", 32);
  }

  contentHash(type: string, data: Uint8Array): Uint8Array {
    return hmac(sha256, this.hashKey, concatBytes(utf8ToBytes(type), new Uint8Array([0]), data));
  }

  /** Encrypts content, filling in the envelope and returning follow-up chunks. */
  sealClip(env: Envelope, content: Uint8Array, chunkSize = CHUNK_SIZE): Chunk[] {
    const parts = split(content, chunkSize);
    env.v = 1;
    env.epoch = this.epoch;
    env.hash = this.contentHash(env.type, content);
    env.size = content.length;
    env.chunks = parts.length;
    env.nonce = randomBytes(24);
    env.payload = xchacha20poly1305(this.contentKey, env.nonce, aad(env)).encrypt(parts[0]);
    return parts.slice(1).map((p, i) => {
      const nonce = randomBytes(24);
      return { id: env.id, index: i + 1, nonce, payload: xchacha20poly1305(this.contentKey, nonce, chunkAad(env, i + 1)).encrypt(p) };
    });
  }

  openClip(env: Envelope): Uint8Array {
    if (env.epoch !== this.epoch) throw new Error(`clip epoch ${env.epoch}, have ${this.epoch}`);
    return xchacha20poly1305(this.contentKey, env.nonce, aad(env)).decrypt(env.payload);
  }

  openChunk(env: Envelope, c: Chunk): Uint8Array {
    if (c.id !== env.id || c.index <= 0 || c.index >= env.chunks) throw new Error("bad chunk");
    return xchacha20poly1305(this.contentKey, c.nonce, chunkAad(env, c.index)).decrypt(c.payload);
  }

  verifyHash(env: Envelope, content: Uint8Array): boolean {
    return content.length === env.size && equal(env.hash, this.contentHash(env.type, content));
  }

  openControl(c: Control): Uint8Array {
    if (c.epoch !== this.epoch) throw new Error("wrong epoch");
    return xchacha20poly1305(this.contentKey, c.nonce, controlAad(c)).decrypt(c.payload);
  }
}

export function aad(e: Envelope): Uint8Array {
  return utf8ToBytes(
    `clipsync/v${e.v}|${e.id}|${e.origin}|${e.clock}|${e.epoch}|${e.type}|${e.size}|${e.chunks}|${e.sent}|${bytesToHex(e.hash)}`,
  );
}

export function chunkAad(e: Envelope, i: number): Uint8Array {
  return concatBytes(aad(e), utf8ToBytes(`|chunk|${i}`));
}

export function controlAad(c: Control): Uint8Array {
  return utf8ToBytes(`clipsync/control|${c.origin}|${c.clock}|${c.epoch}`);
}

export function split(data: Uint8Array, size: number): Uint8Array[] {
  if (data.length === 0) return [new Uint8Array(0)];
  const out: Uint8Array[] = [];
  for (let i = 0; i < data.length; i += size) out.push(data.subarray(i, Math.min(i + size, data.length)));
  return out;
}

function sealedKey(dh1: Uint8Array, dh2: Uint8Array, eph: Uint8Array, sender: Uint8Array, recipient: Uint8Array, info: string) {
  const prk = extract(sha256, concatBytes(dh1, dh2), concatBytes(eph, sender, recipient));
  return expand(sha256, prk, utf8ToBytes(info), KEY_SIZE);
}

/** Encrypts to one device; see crypto.SealTo in Go. */
export function sealTo(sender: Identity, recipient: Uint8Array, plaintext: Uint8Array, info: string): Sealed {
  const ephPriv = randomBytes(32);
  const eph = x25519.getPublicKey(ephPriv);
  const key = sealedKey(x25519.getSharedSecret(ephPriv, recipient), x25519.getSharedSecret(sender.priv, recipient), eph, sender.pub, recipient, info);
  const nonce = randomBytes(24);
  return { eph, sender: sender.pub, nonce, payload: xchacha20poly1305(key, nonce, utf8ToBytes(info)).encrypt(plaintext) };
}

/** Decrypts a sealed message; callers must check s.sender. */
export function openFrom(me: Identity, s: Sealed, info: string): Uint8Array {
  const key = sealedKey(x25519.getSharedSecret(me.priv, s.eph), x25519.getSharedSecret(me.priv, s.sender), s.eph, s.sender, me.pub, info);
  return xchacha20poly1305(key, s.nonce, utf8ToBytes(info)).decrypt(s.payload);
}

export function pairMac(code: Uint8Array, label: string, ...fields: Uint8Array[]): Uint8Array {
  const parts: Uint8Array[] = [utf8ToBytes(label)];
  for (const f of fields) {
    const len = new Uint8Array(4);
    new DataView(len.buffer).setUint32(0, f.length);
    parts.push(len, f);
  }
  return hmac(sha256, code, concatBytes(...parts));
}

export function pairRoom(code: Uint8Array): string {
  return "pair-" + bytesToHex(derive(code, "clipsync pair room", 16));
}

export function equal(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a[i] ^ b[i];
  return d === 0;
}

export const REKEY_INFO = "clipsync rekey v1";
export const WELCOME_INFO = "clipsync pair welcome v1";
