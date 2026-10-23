---
version: 1
slug: "web-app"
primary_target: "web/app"
related_targets: []
---

# Phone PWA (web/app)

Mode: Operate. Users arrive mid-task (share sheet, or a 5-second visit to grab/send one clip). Task: send clipboard in one tap, tap a received clip to copy, pair once. States: unpaired, connecting, online, offline, no-HTTPS (clipboard API missing), empty list, sending, received, copied, error.

## Direction contract

THESIS: The phone is the duplicate copy of a numbered receipt pad: every clip is a numbered leaf, and what you write on one device comes through on the others like carbon. Refuses the default chat-bubble/feed of rounded cards.

OWN-WORLD: Cool NCR-white paper ground, carbon-blue ink for content and primary actions, numbering-machine red for leaf numbers and the COPIED stamp, pale ledger-blue rules, perforated dashed separators between leaves. Dark scheme is the carbon sheet itself: blue-black ground, pale carbon-blue impressions. Workhorse system sans for UI; tabular numerals for leaf numbers.

STORY: The visitor sees the newest leaf at the top, knows from the header whether the book is connected, taps a leaf to copy it, or taps Send clipboard to write a new leaf to every device.

FIRST VIEWPORT: Slim header (ClipSync, connection state, devices). Below, the list of leaves, newest first: red No. 0042 + source device + age, content in ink, perforation between. Bottom thumb bar: full-width carbon-blue "Send clipboard" with secondary Type and Attach buttons.

FORM: carbon-copy duplicate receipt book (my pick, grounded #1; assigned was #5 postal identity program). seed:233d4e03690a5553

SIGNATURE: new leaves arrive as a carbon impression (blur-to-sharp ink transfer); copying stamps the leaf with a red COPIED mark. Destructive Unpair sits alone in settings.
