"use client";

import { useEffect, useState } from "react";

import { Masthead } from "../../components/masthead";
import { resetShared } from "../../components/use-sync";
import { join, parseInvite, type Invite } from "../../lib/pair.ts";
import { load } from "../../lib/state.ts";

type Phase = { kind: "reading" } | { kind: "ready"; inv: Invite } | { kind: "pairing"; inv: Invite } | { kind: "done"; name: string } | { kind: "error"; message: string; inv?: Invite };

export default function Pair() {
  const [phase, setPhase] = useState<Phase>({ kind: "reading" });

  async function run(inv: Invite) {
    setPhase({ kind: "pairing", inv });
    try {
      const s = load();
      const name = await join(s, inv);
      resetShared();
      // The secret lives in the fragment; drop it from history once used.
      history.replaceState(null, "", "/pair");
      setPhase({ kind: "done", name });
    } catch (e) {
      setPhase({ kind: "error", message: (e as Error).message, inv });
    }
  }

  useEffect(() => {
    try {
      const inv = parseInvite(location.hash.slice(1));
      const s = load();
      // Already in a group? Ask before replacing it; otherwise pair right away.
      if (s.group) setPhase({ kind: "ready", inv });
      else run(inv);
    } catch {
      setPhase({ kind: "error", message: "This pairing link is incomplete. Scan the code again, or copy the whole link." });
    }
  }, []);

  return (
    <div className="book">
      <Masthead />
      <section className="sheet" aria-live="polite">
        {phase.kind === "reading" && <h1>Reading the pairing link…</h1>}
        {phase.kind === "ready" && (
          <>
            <h1>Switch to {phase.inv.n}’s group?</h1>
            <p>This phone is already paired with another group. Joining this one replaces it.</p>
            <div className="row">
              <button className="btn btn-primary" type="button" onClick={() => run(phase.inv)}>
                Join {phase.inv.n}’s group
              </button>
              <a className="btn" href="/">
                Cancel
              </a>
            </div>
          </>
        )}
        {phase.kind === "pairing" && (
          <>
            <h1>Pairing with {phase.inv.n}…</h1>
            <p>Keep the pairing screen open on your computer. This takes a second or two.</p>
          </>
        )}
        {phase.kind === "done" && (
          <>
            <h1>Paired with {phase.name}</h1>
            <p>New clips from your computers will appear here. Add ClipSync to your home screen to send from the share sheet.</p>
            <a className="btn btn-primary" href="/">
              Open ClipSync
            </a>
          </>
        )}
        {phase.kind === "error" && (
          <>
            <h1>Pairing didn’t finish</h1>
            <p className="error" role="alert">
              {phase.message}
            </p>
            <div className="row">
              {phase.inv && (
                <button className="btn btn-primary" type="button" onClick={() => run(phase.inv!)}>
                  Try again
                </button>
              )}
              <a className="btn" href="/">
                Back
              </a>
            </div>
          </>
        )}
      </section>
    </div>
  );
}
