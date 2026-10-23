"use client";

import { useEffect, useRef, useState } from "react";

import { Masthead } from "../../components/masthead";
import { useSync } from "../../components/use-sync";

type Phase = "waiting" | "sent" | "nothing" | "unpaired" | "offline";

/** Web Share Target: Share → ClipSync sends the shared text or link. */
export default function Share() {
  const { saved, client } = useSync();
  const [phase, setPhase] = useState<Phase>("waiting");
  const [text, setText] = useState("");
  const done = useRef(false);

  useEffect(() => {
    const q = new URLSearchParams(location.search);
    // Apps put links in different fields; prefer the URL, keep any message.
    const url = q.get("url") ?? "";
    const body = q.get("text") ?? "";
    const combined = url && body && !body.includes(url) ? `${body}\n${url}` : body || url || q.get("title") || "";
    setText(combined.trim());
  }, []);

  useEffect(() => {
    if (!saved || done.current) return;
    if (!saved.group) return setPhase("unpaired");
    if (!text) return setPhase("nothing");
    if (client?.status === "online") {
      done.current = true;
      client.sendText(text);
      setPhase("sent");
    }
  });

  useEffect(() => {
    const t = setTimeout(() => !done.current && setPhase((p) => (p === "waiting" ? "offline" : p)), 10_000);
    return () => clearTimeout(t);
  }, []);

  const others = saved ? saved.devices.filter((d) => d.id !== saved.deviceId).length : 0;

  return (
    <div className="book">
      <Masthead status={client?.status} devices={others} />
      <section className="sheet" aria-live="polite" style={{ position: "relative" }}>
        {phase === "waiting" && <h1>Sending…</h1>}
        {phase === "sent" && (
          <>
            <span className="stamp" aria-hidden>
              SENT
            </span>
            <h1>Sent to {others === 1 ? "1 device" : `${others} devices`}</h1>
          </>
        )}
        {phase === "offline" && (
          <>
            <h1>Couldn’t reach your relay</h1>
            <p>It will send as soon as the connection comes back, as long as this screen stays open.</p>
          </>
        )}
        {phase === "nothing" && <h1>Nothing to send</h1>}
        {phase === "unpaired" && (
          <>
            <h1>Pair ClipSync first</h1>
            <p>
              On your computer run <code>clipsyncd pair</code> and scan the code.
            </p>
          </>
        )}
        {text && phase !== "unpaired" && <p className="leaf-text">{text}</p>}
        <a className="btn" href="/" style={{ marginTop: 12 }}>
          Open ClipSync
        </a>
      </section>
    </div>
  );
}
