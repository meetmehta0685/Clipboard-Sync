// Joiner side of internal/pairing.
import { equal, identityFrom, openFrom, pairMac, pairRoom, utf8ToBytes, WELCOME_INFO } from "./crypto.ts";
import { b64decode, b64urlDecode, decode, encode, type Device, type Message } from "./protocol.ts";
import { relayUrl, save, setGroup, type Saved } from "./state.ts";

export interface Invite {
  v: number;
  r: string; // relay URL the inviter uses
  d: string; // inviter device ID
  n: string; // inviter name
  k: Uint8Array; // inviter public key
  c: Uint8Array; // one-time code
}

export function parseInvite(link: string): Invite {
  const frag = link.includes("#") ? link.slice(link.lastIndexOf("#") + 1) : link.trim();
  const raw = JSON.parse(new TextDecoder().decode(b64urlDecode(frag)));
  const inv: Invite = { ...raw, k: b64decode(raw.k), c: b64decode(raw.c) };
  if (inv.v !== 1 || inv.c.length < 16 || inv.k.length !== 32) throw new Error("This is not a valid ClipSync pairing link.");
  return inv;
}

/** Runs the pairing handshake and stores the received group key. */
export function join(s: Saved, inv: Invite, timeoutMs = 60_000): Promise<string> {
  const me = identityFrom(b64decode(s.priv));
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(relayUrl());
    const timer = setTimeout(() => {
      ws.close();
      reject(new Error("Timed out. Is the pairing screen still open on your computer?"));
    }, timeoutMs);
    const fail = (e: Error) => {
      clearTimeout(timer);
      ws.close();
      reject(e);
    };
    ws.onerror = () => fail(new Error("Could not reach the relay."));
    ws.onopen = () => {
      ws.send(encode({ kind: "hello", hello: { v: 1, room: pairRoom(inv.c), pair: true, device: s.deviceId, since: 0 } }));
      const hello = { device: s.deviceId, name: s.name, kind: "phone", pub: me.pub };
      const mac = pairMac(inv.c, "clipsync pair hello", utf8ToBytes(hello.device), utf8ToBytes(hello.name), utf8ToBytes(hello.kind), hello.pub);
      ws.send(encode({ kind: "pair_hello", pair_hello: { ...hello, mac } }));
    };
    ws.onmessage = (ev) => {
      const m = decode<Message>(ev.data);
      if (m.kind === "error") return fail(new Error(m.error ?? "relay error"));
      const pw = m.pair_welcome;
      if (m.kind !== "pair_welcome" || !pw) return;
      const sealed = pw.sealed;
      const want = pairMac(inv.c, "clipsync pair welcome", sealed.eph, sealed.sender, sealed.nonce, sealed.payload);
      if (!equal(pw.mac, want) || !equal(sealed.sender, inv.k)) return; // not from the inviter
      try {
        const w = decode<{ key: Uint8Array; epoch: number; devices: Device[] }>(new TextDecoder().decode(openFrom(me, sealed, WELCOME_INFO)));
        setGroup(s, w.key, w.epoch, w.devices);
        save(s);
        clearTimeout(timer);
        ws.close();
        resolve(inv.n);
      } catch (e) {
        fail(e as Error);
      }
    };
  });
}
