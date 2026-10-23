# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

People who move text, links, screenshots and small files between their own laptops, desktops and phones many times a day, and who care that their clipboard never leaves their control. They self-host the relay (or use one a friend runs) and install the desktop daemon and the phone PWA themselves. On the phone they are usually mid-task in another app: they arrive from the share sheet or open ClipSync for a few seconds to grab or send one thing.

## Product Purpose

ClipSync makes a copy on one device available on the others within about a second. Desktops sync automatically; phones sync with one tap because iOS and Android block background clipboard access. Success is that sending or grabbing a clip on the phone takes one tap and never makes the user wonder whether it worked.

## Positioning

End-to-end encrypted clipboard sync the user can run entirely on their own infrastructure: the relay only ever sees ciphertext, devices pair by scanning a QR code, and laptops on the same network talk directly over the LAN. Unlike Universal Clipboard or KDE Connect it is cross-platform and honest about the phone limits rather than hiding them.

## Operating Context

- Desktop daemon (`clipsyncd`) runs in the background and exposes a CLI; pairing starts with `clipsyncd pair`, which shows a QR code / link.
- The phone PWA is served by the relay at the same origin; scanning the QR with the phone camera opens `/pair#<invite>`.
- Phone use is brief and interrupted: share sheet → ClipSync, or open, tap a clip to copy, leave.
- The browser clipboard API requires HTTPS and a user gesture; reading the clipboard may prompt the user.

## Capabilities and Constraints

- Clip types: text, images (PNG), small files (default cap 25 MB).
- Phone: receive live clips, tap to copy, "send clipboard", send typed text, send a photo/file, share target for text and links, history (last 100), device list, unpair.
- Pairing is one-way from a desktop invite; the phone cannot invite.
- Removing a device rotates the group key; removed devices see nothing afterwards.
- Requires HTTPS for install, clipboard access and the share target.

## Product Principles

1. One tap for the common action; never make the user hunt.
2. Always show whether a clip actually went out or arrived (connection state is visible, not implied).
3. Be honest about platform limits and explain them in plain words where they bite.
4. Privacy is a fact to state calmly, not a feature to shout about.
5. Fast on a cold start on a mid-range phone.

## Accessibility & Inclusion

WCAG 2.2 AA: contrast, visible focus, 44px minimum touch targets, reduced-motion support, screen-reader labels and live announcements for sent/received clips.
