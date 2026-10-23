"use client";

import { ClipboardPaste, Paperclip, SendHorizontal, Type } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Leaf } from "../components/leaf";
import { Masthead } from "../components/masthead";
import { clipboardAvailable, resetShared, useSync } from "../components/use-sync";
import { toPng } from "../lib/image.ts";
import { forget } from "../lib/state.ts";
import type { Clip, SyncClient } from "../lib/sync.ts";

const MAX_BYTES = 25 * 1024 * 1024;

export default function Home() {
  const { saved, client } = useSync();
  if (!saved) return <div className="book" />;
  if (!saved.group || !client) return <Onboarding />;
  return <Book client={client} />;
}

function Onboarding() {
  const [link, setLink] = useState("");
  const [error, setError] = useState("");
  return (
    <div className="book">
      <Masthead />
      <section className="sheet" aria-labelledby="pair-title">
        <h1 id="pair-title">Pair this phone</h1>
        <p>ClipSync keeps your clipboard in step across your computers and this phone. Everything is encrypted on your devices before it leaves them.</p>
        <ol className="steps">
          <li>
            On your computer, run <code>clipsyncd pair</code>.
          </li>
          <li>Point this phone’s camera at the code it shows and open the link.</li>
        </ol>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const hash = link.trim().split("#")[1];
            if (!hash) return setError("That doesn’t look like a pairing link. Copy the whole link that starts with http.");
            location.href = `/pair#${hash}`;
          }}
        >
          <div className="field">
            <label htmlFor="link">Or paste the pairing link</label>
            <input id="link" className="input" inputMode="url" autoComplete="off" placeholder="https://…/pair#…" value={link} onChange={(e) => setLink(e.target.value)} aria-describedby={error ? "link-error" : undefined} />
          </div>
          {error && (
            <p id="link-error" className="error" role="alert">
              {error}
            </p>
          )}
          <button className="btn btn-primary" type="submit" disabled={!link.trim()}>
            Pair
          </button>
        </form>
        <h2>Why do I tap to send?</h2>
        <p>iOS and Android don’t let any app read the clipboard in the background. Computers sync on their own; on the phone you send with one tap, and new clips from your computers appear here instantly.</p>
      </section>
    </div>
  );
}

function Book({ client }: { client: SyncClient }) {
  const [settings, setSettings] = useState(false);
  const [composer, setComposer] = useState(false);
  const [draft, setDraft] = useState("");
  const [stamp, setStamp] = useState<{ id: string; label: string } | null>(null);
  const [announce, setAnnounce] = useState("");
  const [problem, setProblem] = useState("");
  const [, tick] = useState(0);
  const initial = useRef<Set<string> | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const canCopy = clipboardAvailable();
  const s = client.state;
  const others = s.devices.filter((d) => d.id !== s.deviceId).length;

  if (initial.current === null) initial.current = new Set(client.clips.map((c) => c.id));

  // Refresh the "2 min" ages.
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30_000);
    return () => clearInterval(t);
  }, []);

  // Announce arrivals for screen readers.
  const newest = client.clips[0];
  useEffect(() => {
    if (newest && !newest.local && !initial.current?.has(newest.id)) setAnnounce(`New clip from ${client.deviceName(newest.origin)}`);
  }, [newest?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const flash = (id: string, label: string, message: string) => {
    setStamp({ id, label });
    setAnnounce(message);
    setTimeout(() => setStamp((cur) => (cur?.id === id ? null : cur)), 1600);
  };

  const sent = (c: Clip | null) => {
    setProblem("");
    if (c) flash(c.id, "SENT", others ? `Sent to ${others} ${others === 1 ? "device" : "devices"}` : "Saved. Pair a computer to send it anywhere.");
  };

  async function sendClipboard() {
    setProblem("");
    if (!canCopy) {
      setComposer(true);
      return;
    }
    try {
      if (navigator.clipboard.read) {
        const items = await navigator.clipboard.read();
        for (const item of items) {
          const img = item.types.find((t) => t.startsWith("image/"));
          if (img) return sent(client.publish("image", await toPng(await item.getType(img))));
          if (item.types.includes("text/plain")) {
            const text = await (await item.getType("text/plain")).text();
            if (text.trim()) return sent(client.sendText(text));
          }
        }
      } else {
        const text = await navigator.clipboard.readText();
        if (text.trim()) return sent(client.sendText(text));
      }
      setProblem("Your clipboard is empty. Copy something first, or type it below.");
      setComposer(true);
    } catch {
      setProblem("ClipSync wasn’t allowed to read the clipboard. Paste into the box instead.");
      setComposer(true);
    }
  }

  async function attach(file: File) {
    if (file.size > MAX_BYTES) return setProblem(`That file is ${(file.size / 1048576).toFixed(0)} MB. The limit is 25 MB.`);
    try {
      if (file.type.startsWith("image/")) sent(client.publish("image", await toPng(file)));
      else sent(client.publish("file", new Uint8Array(await file.arrayBuffer()), file.name));
    } catch (e) {
      setProblem((e as Error).message);
    }
  }

  async function copy(c: Clip) {
    if (!canCopy) return;
    try {
      if (c.type === "image" && c.data) {
        await navigator.clipboard.write([new ClipboardItem({ "image/png": new Blob([c.data as BlobPart], { type: "image/png" }) })]);
      } else {
        await navigator.clipboard.writeText(c.text ?? "");
      }
      flash(c.id, "COPIED", "Copied to clipboard");
    } catch {
      setProblem("Copying was blocked by the browser. Long-press the text to copy it instead.");
    }
  }

  return (
    <div className="book">
      <Masthead status={client.status} devices={others} settingsOpen={settings} onSettings={() => setSettings(!settings)} />
      <p className="sr-only" aria-live="polite">
        {announce}
      </p>

      {settings ? (
        <SettingsSheet client={client} />
      ) : (
        <>
          {!canCopy && (
            <p className="notice">
              <strong>Clipboard access needs HTTPS.</strong> You can still read and send clips here. To copy and paste with one tap, open ClipSync over an <code>https://</code> address.
            </p>
          )}
          {problem && (
            <p className="notice error" role="alert">
              {problem}
            </p>
          )}
          {client.clips.length === 0 ? (
            <div className="empty">
              <strong>No clips yet</strong>
              Copy something on your computer and it appears here. Or send this phone’s clipboard with the button below.
            </div>
          ) : (
            <ol className="leaves" aria-label="Clips, newest first">
              {client.clips.map((c) => (
                <Leaf
                  key={c.id}
                  clip={c}
                  from={c.local ? "This phone" : client.deviceName(c.origin)}
                  fresh={!c.local && !initial.current!.has(c.id)}
                  stamp={stamp?.id === c.id ? stamp.label : undefined}
                  canCopy={canCopy}
                  onCopy={copy}
                />
              ))}
            </ol>
          )}

          <div className="bar">
            {composer && (
              <form
                className="composer"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (!draft.trim()) return;
                  sent(client.sendText(draft));
                  setDraft("");
                  setComposer(false);
                }}
              >
                <label className="sr-only" htmlFor="draft">
                  Text to send
                </label>
                <textarea id="draft" className="input" autoFocus placeholder="Type or paste text to send" value={draft} onChange={(e) => setDraft(e.target.value)} />
                <button className="btn btn-primary" type="submit" disabled={!draft.trim()}>
                  <SendHorizontal size={20} strokeWidth={2} aria-hidden /> Send text
                </button>
              </form>
            )}
            <div className="bar-inner">
              <button type="button" className="btn btn-primary" onClick={sendClipboard}>
                <ClipboardPaste size={20} strokeWidth={2} aria-hidden /> Send clipboard
              </button>
              <button type="button" className="icon-btn btn" onClick={() => setComposer(!composer)} aria-label={composer ? "Close text box" : "Type text to send"} aria-expanded={composer}>
                <Type size={20} strokeWidth={2} />
              </button>
              <button type="button" className="icon-btn btn" onClick={() => fileInput.current?.click()} aria-label="Send a photo or file">
                <Paperclip size={20} strokeWidth={2} />
              </button>
              <input
                ref={fileInput}
                type="file"
                hidden
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) attach(f);
                  e.target.value = "";
                }}
              />
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function SettingsSheet({ client }: { client: SyncClient }) {
  const s = client.state;
  const [confirm, setConfirm] = useState(false);
  return (
    <section className="sheet" aria-labelledby="settings-title">
      <h1 id="settings-title">Devices</h1>
      <ul className="devices">
        {s.devices.map((d) => (
          <li key={d.id}>
            <span>
              {d.name}
              {d.id === s.deviceId && " (this phone)"}
            </span>
            <small>{d.kind}</small>
          </li>
        ))}
      </ul>
      <p>To add a computer, run <code>clipsyncd pair</code> on a computer that is already paired. To remove a device, run <code>clipsyncd remove &lt;name&gt;</code>; the group key is rotated so it can’t read anything afterwards.</p>

      <h2>History</h2>
      <p>The last 100 clips are kept on this phone. Images and files are kept only until you close the app.</p>
      <button type="button" className="btn" onClick={() => client.clearHistory()} disabled={client.clips.length === 0}>
        Clear history
      </button>

      <div className="danger-zone">
        <h2>Unpair this phone</h2>
        <p>Deletes this phone’s keys and history. Your computers keep syncing; remove this phone from them with <code>clipsyncd remove</code>.</p>
        {confirm ? (
          <div className="row">
            <button
              type="button"
              className="btn btn-danger"
              onClick={() => {
                resetShared();
                forget();
                location.href = "/";
              }}
            >
              Yes, unpair
            </button>
            <button type="button" className="btn" onClick={() => setConfirm(false)}>
              Keep paired
            </button>
          </div>
        ) : (
          <button type="button" className="btn btn-danger" onClick={() => setConfirm(true)}>
            Unpair this phone…
          </button>
        )}
      </div>
    </section>
  );
}
