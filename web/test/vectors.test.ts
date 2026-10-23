// Cross-language compatibility: decrypt everything Go produced, then
// encrypt the same inputs in JS for internal/crypto TestJSVectors.
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { test } from "node:test";

import { GroupKey, identityFrom, openFrom, pairMac, pairRoom, sealTo, utf8ToBytes } from "../lib/crypto.ts";
import { b64decode, decode, encode, type Envelope } from "../lib/protocol.ts";

const root = new URL("../../", import.meta.url);
const raw = JSON.parse(readFileSync(new URL("testdata/vectors.json", root), "utf8"));
const v = decode<any>(JSON.stringify(raw)); // converts the protocol byte fields
const bytes = (s: string | null) => (s ? b64decode(s) : new Uint8Array(0));

const g = new GroupKey(bytes(raw.group_key), raw.epoch);

test("key derivations match Go", () => {
  assert.equal(g.roomId(), raw.room_id);
  assert.deepEqual(g.roomToken(), bytes(raw.room_token));
  assert.deepEqual(g.contentHash(raw.content_hash.type, bytes(raw.content_hash.data)), bytes(raw.content_hash.hash));
  const pm = raw.pair_mac;
  assert.deepEqual(pairMac(bytes(pm.code), pm.label, ...pm.fields.map(bytes)), bytes(pm.mac));
  assert.equal(pairRoom(bytes(raw.pair_room.code)), raw.pair_room.room);
});

test("decrypts Go clips, chunks and control messages", () => {
  raw.clips.forEach((c: any, i: number) => {
    const env: Envelope = v.clips[i].envelope;
    let out = g.openClip(env);
    for (const ch of v.clips[i].chunks) out = new Uint8Array([...out, ...g.openChunk(env, ch)]);
    assert.deepEqual(out, bytes(c.plaintext), `clip ${i}`);
    assert.ok(g.verifyHash(env, out));
  });
  assert.deepEqual(g.openControl(v.control.control), bytes(raw.control.plaintext));
});

test("rejects tampered Go clips", () => {
  const env: Envelope = { ...v.clips[0].envelope, clock: v.clips[0].envelope.clock + 1 };
  assert.throws(() => g.openClip(env));
});

test("opens a box Go sealed to us", () => {
  const me = identityFrom(bytes(raw.sealed.recipient_priv));
  const pt = openFrom(me, v.sealed.sealed, raw.sealed.info);
  assert.deepEqual(pt, bytes(raw.sealed.plaintext));
});

test("writes JS vectors for Go to verify", () => {
  const clips = raw.clips.map((c: any, i: number) => {
    const src: Envelope = v.clips[i].envelope;
    const env = { id: src.id.replace("0f8e", "1a5c"), origin: "js-device", clock: src.clock, type: src.type, sent: Date.now() } as Envelope;
    const chunks = g.sealClip(env, bytes(c.plaintext), c.chunk_size);
    return { chunk_size: c.chunk_size, plaintext: bytes(c.plaintext), envelope: env, chunks };
  });
  const recipient = identityFrom(bytes(raw.sealed.recipient_priv));
  const sender = identityFrom(new Uint8Array(32).fill(5));
  const plaintext = utf8ToBytes('{"key":"from js"}');
  const out = {
    group_key: g.key,
    epoch: g.epoch,
    room_id: g.roomId(),
    clips,
    sealed: { info: raw.sealed.info, sealed: sealTo(sender, recipient.pub, plaintext, raw.sealed.info), plaintext },
    pair_mac: { mac: pairMac(bytes(raw.pair_mac.code), raw.pair_mac.label, ...raw.pair_mac.fields.map(bytes)) },
    pair_room: { room: pairRoom(bytes(raw.pair_room.code)) },
  };
  // encode() handles Uint8Array anywhere, including "plaintext".
  writeFileSync(new URL("web/test/js_vectors.json", root), encode(out));
});
