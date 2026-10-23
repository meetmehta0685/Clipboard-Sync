"use client";

import { FileDown } from "lucide-react";

import type { Clip } from "../lib/sync.ts";

export function ago(ts: number, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - ts) / 1000));
  if (s < 10) return "just now";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86400) return `${Math.floor(s / 3600)} h`;
  return new Date(ts).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const isLink = (t?: string) => !!t && /^https?:\/\/\S+$/.test(t.trim());

interface Props {
  clip: Clip;
  from: string;
  fresh: boolean;
  stamp?: string;
  canCopy: boolean;
  onCopy: (c: Clip) => void;
}

/** One numbered leaf of the duplicate book. The number is the clip's Lamport clock. */
export function Leaf({ clip, from, fresh, stamp, canCopy, onCopy }: Props) {
  const no = `No. ${String(clip.clock).padStart(4, "0")}`;
  const meta = (
    <span className="leaf-meta">
      <span className="leaf-no">{no}</span>
      <span>{from}</span>
      <time className="leaf-age" dateTime={new Date(clip.created).toISOString()}>
        {ago(clip.created)}
      </time>
    </span>
  );

  if (clip.type === "file") {
    return (
      <li className={`leaf${fresh ? " is-new" : ""}`}>
        <a className="leaf-body" href={clip.url} download={clip.text} aria-disabled={!clip.url} onClick={(e) => !clip.url && e.preventDefault()}>
          {meta}
          <span className="leaf-file">
            <FileDown size={22} strokeWidth={1.75} aria-hidden />
            <span>
              {clip.text} <small>{bytes(clip.size)}</small>
            </span>
          </span>
          <span className="leaf-hint">{clip.url ? "Tap to save" : "File contents are kept only until you close the app"}</span>
        </a>
      </li>
    );
  }

  const label = clip.type === "image" ? "Image" : clip.text;
  return (
    <li className={`leaf${fresh ? " is-new" : ""}`}>
      <button
        type="button"
        className="leaf-body"
        onClick={() => onCopy(clip)}
        disabled={clip.type === "image" && !clip.data}
        aria-label={canCopy ? `Copy ${clip.type === "image" ? "image" : `“${label?.slice(0, 80)}”`} from ${from}` : undefined}
      >
        {meta}
        {clip.type === "image" ? (
          clip.url ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img className="leaf-image" src={clip.url} alt={`Image from ${from}, ${bytes(clip.size)}`} />
          ) : (
            <span className="leaf-hint">Image ({bytes(clip.size)}), no longer in memory</span>
          )
        ) : (
          <p className={`leaf-text${isLink(clip.text) ? " is-link" : ""}`}>{clip.text}</p>
        )}
        {!canCopy && clip.type === "text" && <span className="leaf-hint">Long-press the text to copy it</span>}
      </button>
      {stamp && (
        <span className="stamp" aria-hidden>
          {stamp}
        </span>
      )}
    </li>
  );
}
