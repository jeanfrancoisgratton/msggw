# TODO

## Decisions still open

- [ ] **Webhook integration** — the bridge uses a Mattermost **bot account**
      (REST + WebSocket), per [SOLUTION.md](SOLUTION.md), because a webhook
      cannot read replies, upload files or edit posts. If an incoming webhook is
      still wanted as an extra notify-only output, decide what it should carry
      and where it plugs in. The secret plumbing already handles it: a webhook
      URL is just another reference (`vault:`, `file:`, `env:`, …).
- [ ] Decide whether `thread_per_conversation` should be settable **per routing
      rule** rather than globally. Today a deployment that wants threads in one
      channel and flat posts in another cannot have both.

## Implemented, needs a live phone to confirm

- [ ] Phase 1 — pair, persist the session, reconnect, list conversations
      (`rcs-mm_gw pair` verifies this itself, but it has not been run against a
      real phone).
- [ ] Phase 2 — receive a message and log it.
- [ ] Phase 3 — post an incoming RCS message to Mattermost.
- [ ] Phase 4 — send a Mattermost reply back over RCS.
- [ ] Phase 5 — persistence, deduplication, restart-safe mappings.
- [ ] Phase 6 — media in both directions.

## Not started

- [ ] Reactions, both directions (Phase 7). `libgm` exposes `SendReaction`;
      Mattermost reactions arrive as WebSocket events the bridge currently
      ignores.
- [ ] Typing indicators. `gmessages.Client.SetTyping` exists and is unused;
      incoming `TypingData` events are dropped.
- [ ] Read state from Mattermost back to the phone (the reverse of
      `mark_read_on_bridge`).
- [ ] Starting a **new** conversation from Mattermost.
      `gmessages.Client.StartConversation` exists and is unused: it needs a
      command surface, most likely a slash command or a magic post.
- [ ] Prometheus or similar metrics.
- [ ] systemd unit in the packaging stubs (`__debian`, `__redhat`,
      `__alpine`, `__archlinux`).
- [ ] Google-account (Gaia) pairing as an alternative to the QR flow. It needs
      cookies from a signed-in browser session, which a headless daemon cannot
      obtain on its own; it would have to be a `cookies_ref` secret.
