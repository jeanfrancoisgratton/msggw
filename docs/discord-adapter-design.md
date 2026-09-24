# The Discord adapter: how it was derived, and where it diverges

This documents how `internal/discord` and `internal/discordbridge` came to
be shaped the way they are. Neither package was designed from scratch: both
were reverse-engineered from the existing Google Messages adapter
(`internal/gmessages`) and bridge (`internal/bridge`), pattern for pattern,
substituting Discord's protocol (via
[`github.com/bwmarrin/discordgo`](https://github.com/bwmarrin/discordgo)
v0.29.0) for libgm's. Where the two protocols actually differ, this says so
explicitly — those are the places a reader familiar with the RCS side
should slow down.

This is implementation history and rationale, not a tutorial on Discord or
on this codebase in general; see `docs/SOLUTION.md` for the latter and
`docs/msggw-rcs-mattermost-discord.md` for the original multi-transport
design proposal this implements the first slice of.

---

## 1. Why two new packages, not a retrofit

`internal/bridge` is typed directly to `gmessages.*` throughout — 42
references across its six files, confirmed by grep before writing a line of
Discord code. Retrofitting it into a shared `Transport` interface (as the
original design doc sketches) would mean touching the RCS path, which was
explicitly out of scope: RCS is tabled, not being redesigned right now.

So `internal/discord` and `internal/discordbridge` are new, standalone
packages that mirror the shape of `internal/gmessages`/`internal/bridge`
without sharing code with them. `internal/mattermost` and
`internal/storage` needed **no changes at all** — neither imports
`gmessages`, so both were already protocol-neutral. That is what made this
tractable without touching RCS: only the two backend-specific adapters are
new; the Mattermost-facing half of the bridge is genuinely shared.

## 2. Package-by-package correspondence

| `internal/gmessages` (RCS)                         | `internal/discord`                                    | Divergence |
|-----------------------------------------------------|---------------------------------------------------------|---|
| `Client` wraps `libgm.Client`                        | `Client` wraps `discordgo.Session`                       | — |
| `Config{Session, Logger, LogLevel, PingInterval, ForceRCS}` | `Config{Token, Logger}`                          | No session store: a bot token is static config, not a pairing that expires. No ping-interval knob: discordgo owns gateway heartbeats internally. |
| `queue` (unbounded FIFO, `sync.Cond`)                | `queue` — copied verbatim                                | Each protocol package owns its own; there is no shared queue package in this codebase, so this one is a straight copy, not an import. |
| `Event` / `ReadyEvent` / `MessageEvent` / `ConnectionEvent` / `PairedEvent` / `LoggedOutEvent` / `AlertEvent` | `Event` / `ReadyEvent` / `MessageEvent` / `ConnectionEvent` | No `PairedEvent`/`LoggedOutEvent`: a bot token doesn't get "logged out" the way a phone pairing does. No `AlertEvent` equivalent: Discord has no analog to RCS's phone-status alerts. |
| `handleLibgmEvent` (protocol → `Event` translation) | `onReady` / `onConnect` / `onDisconnect` / `onResumed` / `onMessageCreate` (discordgo `AddHandler` callbacks) | discordgo dispatches by registering one handler per event type rather than one big type-switch; the *translation* work inside each handler plays the same role as the type-switch's cases. |
| Loop prevention: none needed — libgm never echoes a sent message back as if it were new; it arrives as a status update on the same message ID (see `bridge/inbound.go`'s three-way branch: echo / status-update / new) | Loop prevention: `onMessageCreate` drops any message whose author is the bot's own ID | Discord's gateway *does* deliver `MESSAGE_CREATE` for the bot's own sends, unlike libgm. This one behavioral difference is why `discordbridge/inbound.go` is simpler than `bridge/inbound.go`: there is no echo/temporary-ID case and no status-update case to distinguish — every `MessageEvent` reaching the bridge is a genuinely new message from someone else. |
| `SendText`/`SendReply`/`SendMedia` return `SendResult{TmpID}` — the real message ID arrives later, asynchronously, as an ordinary message event | `Send` returns the created `Message` (with its real ID) synchronously | Discord's REST API returns the created message in the same response. This removes the entire `AddPendingOutbound`/`TakePendingOutbound` mechanism from the outbound path — see §3. |
| `Attachment{Name, MimeType, Data}` (outbound), `Media{ID, ThumbnailID, ..., Pending()}` (inbound, has pending/thumbnail states) | `Upload{Name, MimeType, Data}` (outbound), `Attachment{ID, Name, MimeType, URL, Size}` (inbound, no pending state) | Discord attachments are available immediately over a public HTTPS URL as soon as the message exists; there is nothing equivalent to an MMS the phone hasn't finished downloading yet. |
| `Participant{ID, Phone, DisplayName, IsMe}`, populated from the conversation's authoritative participant list | `Participant{ID, DisplayName, IsMe}`, best-effort for a guild channel | See §4. |

| `internal/bridge` (RCS↔Mattermost)                 | `internal/discordbridge`                                 | Divergence |
|-----------------------------------------------------|-------------------------------------------------------------|---|
| `Bridge{tenant, user, ..., conversationLocks}`, one per paired phone | `Bridge{cfg, ..., conversationLocks}`, one per daemon | Discord is one shared bot connection, not per-tenant — see §5. |
| `handleIncomingMessage`: echo / status-update / new (three-way branch) | `handleIncomingMessage`: already-bridged / new (two-way branch) | Simplified per the loop-prevention divergence above; still checks `storage.GetMessage` for a gateway-resume replay of something already bridged. |
| `handleStatusUpdate`/`applyDeliveryStatus`/reaction emoji for delivery state | *(no equivalent)* | Discord gives a bot no delivery/read-receipt signal. `DiscordRoutingConfig` deliberately has no `PostDeliveryStatus` field. |
| `Router` keyed to `gmessages.Conversation`, matching `Rule{ConversationIDs, Phones, NamePattern, GroupsOnly, DirectsOnly}` | `Router` keyed to `discord.Conversation`, matching `DiscordRule{GuildIDs, ChannelIDs, ChannelNamePattern, GroupsOnly, DirectsOnly}` | Same compiled-rule structure and matching semantics; different identifiers because phone numbers have no Discord equivalent. Decided as a new type rather than reusing `Rule`, to keep field names honest instead of overloading `ConversationIDs`/`Phones`. |
| `handleConversationUpdate` (renamed group, changed participants) | *(no equivalent)* | v1 does not watch for Discord channel renames — see §6. |
| `backfill` (`GMessagesConfig.BackfillCount`) | *(no equivalent)* | Out of scope for v1; Discord's REST message-history endpoint would make this straightforward to add later. |

## 3. The synchronous-send simplification

The single biggest structural difference is that `gmessages.Client.Send*`
returns only a temporary ID — the real message ID is not known until the
phone echoes it back, asynchronously, as a `MessageEvent`. That is why
`internal/bridge` needs `storage.AddPendingOutbound`/`TakePendingOutbound`:
a table of "sent but not yet acknowledged" messages, matched up by temporary
ID when the echo arrives, plus a pruning sweep (`pendingOutboundTTL`,
`pruneInterval`) for echoes that never come.

Discord's `ChannelMessageSendComplex` returns the created message — real ID
included — in the same HTTP response. `discord.Client.Send` therefore
returns a `Message` synchronously, and `discordbridge.handleOutgoingPost`
calls `storage.SaveMessage` directly with that ID. There is no pending
table, no temporary-ID matching, and no pruning sweep in
`internal/discordbridge` at all — that entire mechanism from
`internal/bridge` simply has no counterpart here.

## 4. Participants are best-effort for guild channels

`gmessages.Conversation.Participants` is authoritative: the phone hands
over the full participant list up front, because SMS/RCS conversations are
inherently a fixed, small set of phone numbers.

A Discord guild channel has no equivalent cheap call — fetching a channel's
full member list is a separate, heavier API surface this v1 adapter does
not use. `discord.Client.Conversation` (called from
`discordbridge.ensureConversation` the first time a channel is bridged)
therefore leaves `Participants` empty for a guild channel; a DM or group DM
*does* get its full recipient list for free, since Discord returns that as
part of the channel object itself (`Channel.Recipients`).

Practical effect: `conversationHeader`'s "with so-and-so" line is populated
for DMs/group DMs and empty for guild channels. This was an accepted v1
limitation, not an oversight — populating it properly would mean either an
extra `GUILD_MEMBERS` privileged intent and a full member-list fetch per
channel, or growing the list opportunistically from message authors seen
over time. Neither was worth doing before the first useful milestone
(reliable inbound/outbound text) existed.

## 5. Why Discord has no per-tenant list

`gmessages`/`bridge` are per-tenant because each tenant is a different
paired phone with its own Google account and its own session file under
`RootDir`. Discord has no equivalent pairing concept: one bot application,
once invited to however many guilds and given however many DM conversations,
serves all of them through a single gateway connection.

So `config.Discord` is one block at the top of `Config`, structurally
parallel to `Config.Mattermost` (also one shared connection), not to
`Config.Users` (one entry per phone). `runDiscord` in `cmd/discord.go` is
started at most once per daemon generation, alongside — not once per
entry in — the `cfg.Users` loop in `runGeneration`.

This also settled a config-model question raised before implementation
started: should a "user" be keyed by Mattermost identity, with each backend
nested underneath? For Discord specifically, no restructuring was needed to
get that property: the actual per-person/per-destination targeting already
happens through `config.Destination` (shared, unmodified, and already
Mattermost-keyed) inside each `DiscordRule`. `config.UserConfig` — the
RCS-side "one entry per phone" model — was not touched.

### Storage reuse without a schema change

`storage.DB`'s methods are scoped by an opaque `tenant string` parameter on
every call. `internal/discordbridge` uses one fixed value, `"discord"`, in
place of a phone-tenant's name. This works with zero schema changes,
despite the actual SQL column names being historically gmessages-flavored
(`gmessages_conversation_id`, `gmessages_message_id`, etc., visible in
`internal/storage/conversations.go`/`messages.go`) — those names are a
pre-existing cosmetic mismatch, not a functional obstacle: the values stored
are opaque per-tenant strings regardless of which backend produced them, and
renaming the columns was out of scope for this change.

One consequence worth knowing: `mattermost.Client.Listen` is called once
per bridge instance sharing the one `mattermost.Client` — once per
GMessages tenant already, and now once more for Discord. Every post event
fans out to every listener, and each bridge decides relevance via its own
tenant-scoped storage lookup (`GetConversationByRootPost`/
`GetSoleConversationInChannel` returning `ErrNotFound` for a post that
isn't theirs). This is the pre-existing multi-tenant design, unchanged;
Discord's bridge is simply one more consumer of it.

## 6. Known v1 limitations

These were deliberate scope cuts, not gaps discovered after the fact — the
implementation plan called them out before any code was written:

- **No channel-rename tracking.** `internal/discord` registers no handler
  for `CHANNEL_UPDATE`/`GUILD_UPDATE`, so `discordbridge` has no
  `handleConversationUpdate` equivalent. A renamed Discord channel's
  Mattermost thread title stays as it was when first bridged.
- **No backfill.** A newly bridged channel starts empty, unlike GMessages'
  `backfill_count`. Discord's REST message-history endpoint would make this
  a small, self-contained addition later.
- **No delivery/read-receipt reflection**, because Discord bots don't get
  one — this is a protocol fact, not a cut corner.
- **Embed-only/sticker-only messages are dropped silently.**
  `Message.HasContent()` treats a message with no text and no attachments
  as nothing to post. A message that is only a Discord embed or a sticker
  currently vanishes rather than being represented some other way.
- **No special handling for other bots' messages.** Only the daemon's own
  bot ID is filtered for loop prevention; a message from a different bot or
  webhook integration in the same channel is bridged like any other message.

## 7. discordgo API surface used

Pinned at `github.com/bwmarrin/discordgo v0.29.0`. For a future reader
tracing behavior back to the library: `discordgo.New`, `Session.Open`/
`Close`, `Session.AddHandler` (for `Ready`, `Connect`, `Disconnect`,
`Resumed`, `MessageCreate`), `Session.ChannelMessageSendComplex`,
`Session.Channel`, `Session.State.User`, and the `IntentGuildMessages` /
`IntentDirectMessages` / `IntentMessageContent` gateway intents (the last
one is what makes a message's actual text visible to the bot at all, as of
Discord API v10 — without it, `Message.Content` arrives empty).
