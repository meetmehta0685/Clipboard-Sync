# Design

The phone PWA (`web/`) is a **carbon-copy duplicate book**: every clip is a numbered leaf, and what you write on one device comes through on the others like carbon. The world lends type, palette, density and one signature move; layout, navigation and controls stay standard web components.

## Color

Restrained strategy: tinted neutrals from the paper plus carbon ink, with numbering-machine red as the only second hue. Defined as CSS custom properties in `web/app/globals.css`.

| Token | Light (top sheet) | Dark (carbon sheet) | Job |
|---|---|---|---|
| `--paper` | `#f2f4f5` | `#0f1428` | Page ground, masthead, thumb bar |
| `--paper-pressed` | `#e4e9ee` | `#182042` | Pressed rows, `code` ground |
| `--sheet` | `#fbfcfc` | `#131a33` | Inputs, notices, image mats |
| `--ink` | `#1e3687` | `#b7c4f3` | Body text, primary button, focus ring, selection |
| `--ink-soft` | `#46578f` | `#93a3d9` | Secondary text, metadata |
| `--ink-faint` | `#56659a` | `#7787bd` | Hints, placeholders (≥4.5:1) |
| `--red` | `#b8232f` | `#ff858b` | Leaf numbers, list markers, stamps, caret, destructive |
| `--rule` | `#c5d0e4` | `#2b3560` | Perforations, hairlines, input borders |

State dots are the only other colors: online `#1f8a4c`, connecting `#c98a12`, offline `--red`. Light or dark follows `prefers-color-scheme`; dark is the carbon sheet itself, not an inverted page.

Measured contrast (AA): body ink 9.9:1 light / 10.6:1 dark, leaf numbers 5.7 / 7.8, metadata 6.3 / 7.4, hints 5.1 / 5.2, primary button 10.2 / 10.6.

## Type

System UI stack (`ui-sans-serif, -apple-system, …`), the right voice for a screen used in five-second visits. One family; hierarchy from size and weight only.

- Leaf text 17px / 1.45, clamped to five lines.
- Headings 22px, weight 700, tracking -0.02em, `text-wrap: balance`.
- Metadata 13px. Leaf numbers 13px, weight 650, `tabular-nums`, tracking 0.04em, in red, formatted `No. 0042` from the clip's Lamport clock (real data, not decoration).
- Monospace only for commands (`clipsyncd pair`).

## Layout

- Single column, max 560px, centered. Sticky masthead (wordmark, connection status, settings). Fixed thumb bar at the bottom with safe-area padding.
- Leaves are full-width rows separated by a 2px dashed `--rule` perforation. No cards, no shadows on rows.
- Spacing: 16px gutters, 14/16px leaf padding, 10px gaps between controls.
- Touch targets ≥ 44px; buttons 48px tall, 12px radius.

## Components

- **Leaf**: one `<button>` per clip (tap = copy), or a download link for files. Meta row: number, source device, age (right-aligned, tabular).
- **Primary button**: solid `--ink` with `--on-ink` text. Secondary: 1.5px ink outline. Destructive: red outline, isolated under a rule in Settings, with a confirm step.
- **Notice**: `--sheet` panel with rule border for platform limits (no HTTPS) and errors (`.error` turns text red).
- **Status**: dot plus words, never color alone; `role="status"`.
- Icons from `lucide-react`, 20–22px, stroke 1.75–2.

## Motion

One authored moment and one mark:

- **Carbon impression**: a newly arrived leaf goes from blur 2.5px / 45% opacity to sharp in 520ms, `cubic-bezier(0.16, 1, 0.3, 1)`. It starts visible.
- **Stamp**: `COPIED` / `SENT` in a red outlined box rotated -7°, landing in two frames (`steps(2)`, 120ms) like a hand stamp, cleared after 1.6s.
- `prefers-reduced-motion` removes all animation.

## Accessibility

WCAG 2.2 AA. Visible 2px focus ring in `--focus`; themed `::selection` and caret; polite live region announces new clips, copies and sends; every icon-only button has a label; errors use `role="alert"` and name the recovery.
