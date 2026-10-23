// Browser port of the sync engine (internal/engine), trimmed to what a phone
// needs: receive, send on demand, chunking, dedupe, Lamport ordering, rekey.
import { CHUNK_SIZE, equal, openFrom, REKEY_INFO, uuid, type GroupKey } from "./crypto.ts";
import { decode, encode, type Chunk, type Device, type Envelope, type Message } from "./protocol.ts";
import { groupKey, identity, loadHistory, relayUrl, save, saveHistory, setGroup, type Saved, type StoredClip } from "./state.ts";

export type Status = "offline" | "connecting" | "online";

export interface Clip extends StoredClip {
  url?: string; // object URL for images (memory only)
  data?: Uint8Array; // image/file bytes (memory only)
}

interface Partial {
  env: Envelope;
  parts: (Uint8Array | null)[];
  got: number;
  touched: number;
  asks: number;
}

const decoder = new TextDecoder();
const encoder = new TextEncoder();

export class SyncClient extends EventTarget {
  status: Status = "offline";
  clips: Clip[];
  private ws: WebSocket | null = null;
  private seen = new Set<string>();
  private partials = new Map<string, Partial>();
  private outbox = new Map<string, Chunk[]>();
  private backoff = 500;
  private stopped = false;
  private timer: ReturnType<typeof setInterval> | null = null;

  constructor(private s: Saved) {
    super();
    this.clips = loadHistory();
    for (const c of this.clips) this.seen.add(c.id);
    // The clock must never fall behind a clip we already hold.
    this.observe(this.clips.reduce((m, c) => Math.max(m, c.clock), 0));
  }

  get state() {
    return this.s;
  }

  private emit() {
    this.dispatchEvent(new Event("change"));
  }

  private setStatus(s: Status) {
    this.status = s;
    this.emit();
  }

  start() {
    this.stopped = false;
    this.connect();
    this.timer = setInterval(() => this.sweep(), 1000);
    document.addEventListener("visibilitychange", this.onVisible);
  }

  stop() {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
    document.removeEventListener("visibilitychange", this.onVisible);
    this.ws?.close();
  }

  // Mobile browsers kill sockets in the background; reconnect on return.
  private onVisible = () => {
    if (document.visibilityState === "visible" && this.status !== "online") {
      this.backoff = 500;
      this.ws?.close();
      this.connect();
    }
  };

  private connect() {
    const g = groupKey(this.s);
    if (!g || this.stopped) return;
    this.setStatus("connecting");
    const ws = new WebSocket(relayUrl());
    this.ws = ws;
    ws.onopen = () => {
      ws.send(encode({ kind: "hello", hello: { v: 1, room: g.roomId(), token: g.roomToken(), device: this.s.deviceId, gen: this.s.gen, since: this.s.seq } }));
    };
    ws.onmessage = (ev) => {
      try {
        this.handle(decode<Message>(ev.data));
      } catch (err) {
        console.warn("clipsync: bad message", err);
      }
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.setStatus("offline");
      if (this.stopped) return;
      setTimeout(() => this.connect(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, 30_000);
    };
  }

  private send(m: Message) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(encode(m));
  }

  private handle(m: Message) {
    if (m.seq && m.gen && m.kind !== "chunk") {
      this.s.seq = m.gen === this.s.gen ? Math.max(this.s.seq, m.seq) : m.seq;
      this.s.gen = m.gen;
      save(this.s);
    }
    switch (m.kind) {
      case "ready":
        this.backoff = 500;
        if (m.gen && m.gen !== this.s.gen) {
          this.s.gen = m.gen;
          this.s.seq = 0;
          save(this.s);
        }
        this.setStatus("online");
        return;
      case "error":
        console.warn("relay:", m.error);
        return;
      case "clip":
        return m.clip && this.onClip(m.clip);
      case "chunk":
        return m.chunk && this.onChunk(m.chunk);
      case "need":
        return m.need && this.onNeed(m.need.id, m.need.indexes, m.need.from);
      case "control":
        return m.control && this.onControl(m.control);
      case "rekey":
        return m.rekey && this.onRekey(m.rekey);
    }
  }

  private observe(clock: number) {
    if (clock > this.s.clock) {
      this.s.clock = clock;
      save(this.s);
    }
  }

  private onClip(env: Envelope) {
    this.observe(env.clock);
    if (env.origin === this.s.deviceId || this.seen.has(env.id)) return;
    const g = groupKey(this.s);
    if (!g || env.epoch !== g.epoch || env.size > 50 * 1024 * 1024) return;
    let first: Uint8Array;
    try {
      first = g.openClip(env);
    } catch {
      return; // forged or corrupted; a good copy may still arrive
    }
    this.seen.add(env.id);
    if (env.chunks <= 1) return this.deliver(g, env, first);
    const parts: (Uint8Array | null)[] = new Array(env.chunks).fill(null);
    parts[0] = first;
    this.partials.set(env.id, { env, parts, got: 1, touched: Date.now(), asks: 0 });
  }

  private onChunk(c: Chunk) {
    const p = this.partials.get(c.id);
    const g = groupKey(this.s);
    if (!p || !g || p.parts[c.index]) return;
    try {
      p.parts[c.index] = g.openChunk(p.env, c);
    } catch {
      return;
    }
    p.got++;
    p.touched = Date.now();
    if (p.got < p.parts.length) return;
    this.partials.delete(c.id);
    const out = new Uint8Array(p.parts.reduce((n, x) => n + x!.length, 0));
    let off = 0;
    for (const x of p.parts) {
      out.set(x!, off);
      off += x!.length;
    }
    this.deliver(g, p.env, out);
  }

  private sweep() {
    const now = Date.now();
    for (const [id, p] of this.partials) {
      if (now - p.touched < 2000) continue;
      if (++p.asks > 5) {
        this.partials.delete(id);
        continue;
      }
      p.touched = now;
      const missing = p.parts.flatMap((x, i) => (x ? [] : [i]));
      this.send({ kind: "need", need: { id, indexes: missing, from: this.s.deviceId } });
    }
  }

  private onNeed(id: string, indexes: number[], from: string) {
    const chunks = this.outbox.get(id);
    if (!chunks || from === this.s.deviceId) return;
    for (const i of indexes) if (chunks[i - 1]) this.send({ kind: "chunk", chunk: chunks[i - 1] });
  }

  private deliver(g: GroupKey, env: Envelope, data: Uint8Array) {
    if (!g.verifyHash(env, data)) return;
    const clip: Clip = { id: env.id, origin: env.origin, type: env.type, size: env.size, clock: env.clock, created: Date.now(), local: false };
    if (env.type === "text") clip.text = decoder.decode(data);
    else if (env.type === "image") {
      clip.data = data;
      clip.url = URL.createObjectURL(new Blob([data as BlobPart], { type: "image/png" }));
    } else {
      const nul = data.indexOf(0);
      if (nul < 0) return; // malformed: files are "name\0bytes"
      clip.text = decoder.decode(data.subarray(0, nul));
      clip.data = data.subarray(nul + 1);
      clip.url = URL.createObjectURL(new Blob([clip.data as BlobPart]));
    }
    this.add(clip);
  }

  private add(clip: Clip) {
    this.clips = [clip, ...this.clips.filter((c) => c.id !== clip.id)].sort((a, b) => b.clock - a.clock || (b.origin > a.origin ? 1 : -1));
    saveHistory(this.clips.map(({ url, data, ...rest }) => rest));
    this.emit();
  }

  private onControl(c: { origin: string; clock: number; epoch: number; nonce: Uint8Array; payload: Uint8Array }) {
    const g = groupKey(this.s);
    if (!g || c.origin === this.s.deviceId) return;
    this.observe(c.clock);
    try {
      const p = decode<{ type: string; devices: Device[] }>(decoder.decode(g.openControl(c)));
      if (p.type !== "devices") return;
      for (const d of p.devices) if (!this.s.devices.some((x) => x.id === d.id)) this.s.devices.push(d);
      save(this.s);
      this.emit();
    } catch {
      /* not for us */
    }
  }

  private onRekey(r: { to: string; from: string; epoch: number; sealed: { eph: Uint8Array; sender: Uint8Array; nonce: Uint8Array; payload: Uint8Array } }) {
    const g = groupKey(this.s);
    if (r.to !== this.s.deviceId || !g || r.epoch <= g.epoch) return;
    const sender = this.s.devices.find((d) => d.id === r.from);
    if (!sender || !equal(sender.pub, r.sealed.sender)) return;
    try {
      const p = decode<{ key: Uint8Array; epoch: number; devices: Device[] }>(decoder.decode(openFrom(identity(this.s), r.sealed, REKEY_INFO)));
      if (p.epoch !== r.epoch) return;
      setGroup(this.s, p.key, p.epoch, p.devices);
      this.ws?.close(); // reconnect into the new room
    } catch {
      /* ignore */
    }
  }

  /** Encrypts and sends a clip to every other device. */
  publish(type: Envelope["type"], content: Uint8Array, name?: string): Clip | null {
    const g = groupKey(this.s);
    if (!g) return null;
    const plain = type === "file" ? new Uint8Array([...encoder.encode(name ?? "file"), 0, ...content]) : content;
    save(this.s); // pull in a newer clock another tab may have stored
    this.s.clock += 1;
    save(this.s);
    const env = { id: uuid(), origin: this.s.deviceId, clock: this.s.clock, type, sent: Date.now() } as Envelope;
    const chunks = g.sealClip(env, plain, CHUNK_SIZE);
    this.seen.add(env.id);
    this.outbox.set(env.id, chunks);
    if (this.outbox.size > 8) this.outbox.delete(this.outbox.keys().next().value!);
    this.send({ kind: "clip", clip: env });
    for (const c of chunks) this.send({ kind: "chunk", chunk: c });

    const clip: Clip = { id: env.id, origin: env.origin, type, size: plain.length, clock: env.clock, created: Date.now(), local: true };
    if (type === "text") clip.text = decoder.decode(content);
    else {
      clip.text = name;
      clip.data = content;
      clip.url = URL.createObjectURL(new Blob([content as BlobPart], { type: type === "image" ? "image/png" : undefined }));
    }
    this.add(clip);
    return clip;
  }

  sendText(text: string) {
    return this.publish("text", encoder.encode(text));
  }

  deviceName(id: string): string {
    if (id === this.s.deviceId) return "This phone";
    return this.s.devices.find((d) => d.id === id)?.name ?? "Unknown device";
  }

  clearHistory() {
    this.clips = [];
    saveHistory([]);
    this.emit();
  }
}
