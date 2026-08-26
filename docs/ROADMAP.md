# ROADMAP, INCLUDED FEATURES

## Current version

```bash
# message-gateway -v
message-gateway version 1.0.0-1 (2026.08.16)
```

## MVP phases

The phases are the ones defined in [SOLUTION.md](SOLUTION.md). "Written" means
the code exists, compiles and is unit-tested where that is possible without a
phone; it does not mean it has been run against real hardware.

| Phase | Feature | Slated for | Status | Comments |
|---|---|---|---|---|
| 1 | Pairing, session persistence, reconnect, list conversations | 1.0.0 | written | `message-gateway pair` authenticates with Google-account cookies, stores the session, reconnects with it and prints the conversation list as its own success check |
| 2 | Receive a message and log it | 1.0.0 | written | `internal/gmessages` translates libgm events into a small neutral vocabulary |
| 3 | Post an incoming message to Mattermost | 1.0.0 | written | bot account over REST; routing decides the destination |
| 4 | Send a Mattermost reply back over RCS | 1.0.0 | written | WebSocket listener, thread → conversation lookup, `libgm.SendMessage` |
| 5 | SQLite mappings, deduplication, restart safety | 1.0.0 | written | conversations, messages, pending outbound; migrations are versioned |
| 6 | Media both ways | 1.0.0 | written | download from Google, upload to Mattermost, and the reverse |
| 7 | Delivery state | 1.0.0 | written | shown as post reactions when `routing.post_delivery_status` is on |
| 7 | Reactions | — | not started | `libgm.SendReaction` is available; Mattermost reaction events are ignored |
| 7 | Replies / quoting | — | partial | outbound reply mapping is narrow; see [ISSUES.md](ISSUES.md) |
| 7 | Read state | — | partial | phone-side only, via `gmessages.mark_read_on_bridge` |
| 7 | Typing indicators | — | not started | `SetTyping` exists and is unused |
| 7 | Group conversations | 1.0.0 | written, unverified | storage does not assume one number per conversation |

## Beyond the MVP

| Feature | Slated for | Status | Comments |
|---|---|---|---|
| Start a new conversation from Mattermost | — | not started | `StartConversation` exists; needs a command surface |
| Per-rule threading | — | not started | see [TODO.md](TODO.md) |
| Optional notify-only webhook output | — | undecided | see [TODO.md](TODO.md) |
| systemd unit in the packaging stubs | — | not started | |
| Metrics | — | not started | |
| Multi-tenancy (several people pairing their own phone against one deployment) | — | written | see [MULTI-TENANCY.md](MULTI-TENANCY.md); `users: []` config, tenant-scoped storage/bridge, per-user `pair`/`logout`/`status`, group-channel auto-creation |
