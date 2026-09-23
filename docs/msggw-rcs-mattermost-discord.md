# msggw: Mattermost ↔ RCS ↔ Discord

## Goal

Extend `msggw` from its current Google Messages/RCS ↔ Mattermost gateway into a **multi-transport messaging gateway**, with Discord as another supported transport.

The important architectural direction is:

```text
                         ┌─────────────────┐
                         │    msggw core   │
                         │                 │
                         │ routing         │
                         │ identities      │
                         │ conversations   │
                         │ persistence     │
                         │ event handling  │
                         └────────┬────────┘
                                  │
                ┌─────────────────┼─────────────────┐
                │                 │                 │
                ▼                 ▼                 ▼
          Mattermost          Google Msgs       Discord
           adapter              adapter          adapter
                │                 │                 │
                ▼                 ▼                 ▼
          MM REST/WS           libgm             REST
                                                  + Gateway
```

The core should not care whether a message originated from RCS, Discord, or Mattermost. Each platform-specific adapter translates its native protocol into a common internal model.

---

## 1. Discord is a feasible addition to msggw

Discord is actually a relatively clean platform to integrate compared with Google Messages.

A Discord integration has well-defined:

- REST APIs
- WebSocket Gateway
- event types
- authentication
- globally unique object IDs
- rate limits
- reconnect/resume mechanisms

The `msggw` Discord adapter therefore does **not** need to reproduce Discord's server-side architecture. It is simply a Go client of Discord's APIs.

Go is entirely appropriate for this.

Discord itself uses Elixir/BEAM extensively for its server-side real-time infrastructure because of its enormous concurrency and state-distribution requirements. That does not imply that a Discord client integration should use Elixir.

---

# 2. The core abstraction should be a transport adapter

Avoid making Discord a special case in the core:

```go
if platform == Discord {
    ...
} else if platform == RCS {
    ...
}
```

Instead, define a transport-oriented interface.

Conceptually:

```go
type Transport interface {
    Connect() error
    Disconnect() error

    Receive() <-chan Message
    Send(Message) error

    Conversations() ([]Conversation, error)

    // Potential future operations:
    Edit(...)
    Delete(...)
    React(...)
    Typing(...)
}
```

The exact Go interface can evolve, but the architectural principle is important:

> Platform-specific protocol details belong in adapters; routing and message semantics belong in the core.

The adapters would be approximately:

```text
Mattermost adapter
    ├── Mattermost REST
    └── Mattermost WebSocket

Google Messages adapter
    └── libgm
         ├── RCS
         ├── MMS
         └── SMS

Discord adapter
    ├── Discord REST API
    └── Discord Gateway WebSocket
```

---

# 3. Normalize messages

The core should work with a platform-neutral message model.

For example:

```go
type Message struct {
    ID             string
    Platform       Platform
    ConversationID string
    Sender         Contact
    Body           string
    Attachments    []Attachment
    Timestamp      time.Time
}
```

The actual structure should eventually be richer, but the important property is that:

```text
Discord message
       │
       ▼
Discord adapter
       │
       ▼
normalized Message
       │
       ▼
msggw core
       │
       ▼
Mattermost adapter
       │
       ▼
Mattermost message
```

The same mechanism works in the opposite direction:

```text
RCS
 │
 ▼
Google Messages adapter
 │
 ▼
normalized Message
 │
 ▼
msggw core
 │
 ▼
Discord adapter
 │
 ▼
Discord message
```

This makes the gateway a messaging transport system rather than an RCS-specific bridge.

---

# 4. Conversation should be the abstraction, not phone numbers

RCS/SMS naturally suggests a conversation model such as:

```text
conversation
 ├── Alice
 ├── Bob
 └── messages
```

Discord is different.

Discord has:

```text
Guild
 ├── #general
 ├── #development
 └── #offtopic
```

A user can belong to many guilds, and each guild contains multiple channels.

Mattermost similarly has:

```text
Team
 ├── channel-a
 ├── channel-b
 └── channel-c
```

Therefore `msggw` should use a generic **Conversation** abstraction.

Conceptually:

```text
Conversation
    │
    ├── platform
    │      discord
    │
    ├── account
    │      my-discord-account
    │
    ├── remote_id
    │      Discord channel ID
    │
    ├── name
    │      #development
    │
    └── participants
```

Then the mappings become:

```text
RCS:
    Conversation = SMS/RCS conversation

Discord:
    Conversation = guild + channel

Mattermost:
    Conversation = team + channel
```

The core can therefore route messages without knowing the native hierarchy of each platform.

---

# 5. Identity mapping

A second important abstraction is identity.

The same person may appear as:

```text
Discord:
    Discord user ID

RCS:
    +1-514-555-1234

Mattermost:
    alice
```

`msggw` should not attempt to automatically decide that these identities represent the same human.

Instead, maintain explicit identity mappings.

Conceptually:

```text
Identity
──────────────
person: alice

    mattermost → alice
    discord    → 123456789012345678
    rcs        → +15145551234
```

This gives the routing layer a stable concept of a person without coupling it to any particular messaging platform.

It also avoids making potentially incorrect assumptions about identity.

---

# 6. Discord adapter responsibilities

The Discord adapter should encapsulate Discord-specific behavior.

A likely responsibility breakdown is:

```text
Discord adapter
 ├── Gateway WebSocket
 ├── authentication
 ├── heartbeat
 ├── sequence numbers
 ├── reconnect/resume
 ├── guild discovery
 ├── channel discovery
 ├── Discord object IDs
 ├── event decoding
 ├── REST operations
 ├── rate-limit handling
 └── Discord event → msggw Message translation
```

The core should not need to know that Discord has a Gateway, heartbeats, guilds, or Discord-specific event names.

For example:

```text
Discord Gateway
      │
      │ MESSAGE_CREATE
      ▼
Discord adapter
      │
      │ normalized Message
      ▼
msggw core
```

Likewise:

```text
msggw core
      │
      │ normalized Message
      ▼
Discord adapter
      │
      │ Discord REST/API operation
      ▼
Discord
```

---

# 7. Google Messages remains another adapter

The existing Google Messages/RCS implementation should follow exactly the same model.

```text
Google Messages
       │
       ▼
    libgm
       │
       ▼
Google Messages adapter
       │
       │ normalized Message
       ▼
    msggw core
```

The core should not contain RCS-specific logic.

The adapter owns:

- Google Messages session handling
- RCS
- MMS
- SMS
- libgm-specific behavior
- Google Messages identifiers
- Google Messages event translation

This keeps the existing RCS implementation from dictating the architecture of future transports.

---

# 8. Mattermost should also be treated as a transport

Although Mattermost is currently the central destination for `msggw`, architecturally it should be treated as another transport.

That gives:

```text
                 msggw core
                     │
        ┌────────────┼────────────┐
        │            │            │
        ▼            ▼            ▼
   Mattermost      RCS         Discord
    adapter       adapter       adapter
```

This is more flexible than thinking of the application as:

```text
RCS → Mattermost
```

Instead, the application becomes:

```text
                    msggw
                      │
             message routing core
                      │
       ┌──────────────┼──────────────┐
       ▼              ▼              ▼
  Mattermost         RCS          Discord
```

That makes future transports possible without redesigning the core.

---

# 9. Example message flows

## RCS → Mattermost

```text
Android / Google Messages
          │
          ▼
     libgm / RCS
          │
          ▼
 Google Messages adapter
          │
          │ normalized Message
          ▼
      msggw core
          │
          │ routing
          ▼
 Mattermost adapter
          │
          ▼
      Mattermost
```

## Mattermost → RCS

```text
Mattermost
    │
    ▼
Mattermost adapter
    │
    │ normalized Message
    ▼
msg gw core
    │
    │ routing
    ▼
Google Messages adapter
    │
    ▼
libgm
    │
    ▼
RCS
```

## Discord → Mattermost

```text
Discord Gateway
      │
      ▼
Discord adapter
      │
      │ normalized Message
      ▼
msggw core
      │
      │ routing
      ▼
Mattermost adapter
      │
      ▼
Mattermost
```

## Mattermost → Discord

```text
Mattermost
      │
      ▼
Mattermost adapter
      │
      │ normalized Message
      ▼
msggw core
      │
      │ routing
      ▼
Discord adapter
      │
      ▼
Discord REST / Gateway
      │
      ▼
Discord
```

---

# 10. Discord's server model is not a problem for the gateway

Discord internally has a very different architecture from `msggw`.

Discord's own server-side infrastructure uses a persistent Gateway and a large-scale real-time event-distribution system. Guilds and client sessions are represented by Elixir processes, allowing events to be distributed efficiently to connected users.

`msggw` does not need to reproduce any of that.

From `msggw`'s perspective:

```text
Discord
   │
   │ WebSocket + REST
   ▼
Discord adapter
   │
   ▼
normal msggw events
```

The adapter only needs to maintain the state necessary for its Discord account/session.

This is an important architectural distinction:

> Discord's server architecture solves Discord's scale problem. The msggw Discord adapter solves a protocol-integration problem.

Go remains entirely reasonable for the latter.

---

# 11. Existing projects worth examining

There is existing work in this general space.

### Matterbridge

Matterbridge already supports bridging Mattermost and Discord, among many other messaging systems.

It is useful as a reference for the general concept of a multi-protocol messaging bridge.

### mautrix-gmessages

`mautrix-gmessages` is particularly relevant conceptually because it implements a Google Messages ↔ Matrix bridge and is written in Go.

Its architecture is useful to study for:

- Google Messages integration
- RCS/SMS handling
- message normalization
- account/session handling
- bridge semantics

These projects solve somewhat different problems from `msggw`, but they demonstrate that the required integrations are technically practical.

---

# 12. Recommended long-term architecture

The desired end state should be something like:

```text
                         ┌───────────────────────┐
                         │       msggw core      │
                         │                       │
                         │ routing               │
                         │ identities            │
                         │ conversations         │
                         │ message normalization │
                         │ persistence            │
                         │ deduplication         │
                         │ retry                  │
                         │ event handling        │
                         └───────────┬───────────┘
                                     │
             ┌───────────────────────┼───────────────────────┐
             │                       │                       │
             ▼                       ▼                       ▼
      Mattermost adapter      Google Messages adapter    Discord adapter
             │                       │                       │
             ▼                       ▼                       ▼
       MM REST/WS                   libgm               REST + Gateway
                                     │                       │
                               ┌─────┼─────┐             Discord
                               ▼     ▼     ▼
                              SMS   MMS    RCS
```

The critical design rule is:

> **Adapters know platforms. The core knows messages, conversations, identities, routing, and delivery semantics.**

That gives `msggw` room to grow without turning the core into a collection of platform-specific conditionals.

---

# 13. Practical implementation order

A sensible implementation sequence would be:

1. **Formalize the common message/conversation model.**
2. **Separate the existing Google Messages implementation behind an adapter interface.**
3. **Treat Mattermost as an adapter as well.**
4. **Implement the Discord adapter independently.**
5. Add Discord Gateway connection/reconnection/resume handling.
6. Translate Discord `MESSAGE_CREATE` and related events into the normalized model.
7. Implement outbound Discord message delivery.
8. Add guild/channel discovery and configuration.
9. Add identity mapping.
10. Add attachments, edits, deletes, reactions, typing indicators, etc. as the common model evolves.

Do not try to model every Discord feature on day one.

The first useful milestone is simply:

```text
RCS ───────┐
           │
           ▼
       msggw core
           │
           ▼
      Mattermost

Discord ───┘
```

with reliable inbound/outbound text messages and explicit conversation mappings.

From there, richer Discord functionality can be added without changing the fundamental architecture.
