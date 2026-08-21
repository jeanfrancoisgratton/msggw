# Google Messages / RCS ↔ Mattermost Gateway

## Objective

Build a self-hosted gateway that allows SMS, MMS, and especially **RCS** conversations from an Android phone to be surfaced inside a Mattermost server, with the ability to reply from Mattermost.

Target flow:

```text
Android phone / Google Messages
            ⇅
      SMS / MMS / RCS
            ⇅
       bridge daemon
            ⇅
        Mattermost
```

The critical requirement is **RCS support**. A solution limited to Android's conventional SMS APIs is therefore insufficient.

---

## Initial SMS/MMS Approach

A straightforward SMS/MMS implementation can be built using an Android SMS gateway such as **SMSGate**.

That architecture would look like:

```text
Android phone
    │
    │ SMS / MMS
    ▼
SMSGate
    │
    │ REST API + Webhooks
    ▼
Go bridge daemon
    │
    │ Mattermost REST API / Webhooks
    ▼
Mattermost
```

This approach provides:

- inbound SMS
- outbound SMS
- inbound MMS
- MMS attachment forwarding
- SMS delivery-status events
- straightforward HTTP integration

However, it does **not** solve the main requirement.

### Blocking limitation

Android's conventional SMS/MMS APIs do not expose arbitrary personal **RCS** conversations.

SMSGate likewise does not provide access to RCS messages.

Therefore:

| Capability | SMSGate-style solution |
|---|---:|
| Receive SMS | Yes |
| Send SMS | Yes |
| Receive MMS | Yes |
| Send MMS | Limited / implementation-dependent |
| Receive RCS | No |
| Send RCS | No |

Because RCS is required, this architecture should not be used as the primary solution.

---

# Selected Solution: Google Messages Web Protocol

The better approach is to integrate with **Google Messages itself**, using the same protocol that powers Google Messages for Web / paired devices.

The most useful existing implementation is:

```text
mautrix/gmessages
```

Its Google Messages client implementation is available as a reusable Go package:

```text
go.mau.fi/mautrix-gmessages/pkg/libgm
```

`libgm` implements the Google Messages web protocol and exposes functionality for:

- conversation listing
- message history
- incoming message events
- sending messages
- SMS
- RCS
- media upload/download
- reactions
- replies
- read state
- typing state

A separate project, **OpenMessage**, already demonstrates using `libgm` outside Matrix as a normal Go application for interacting with Google Messages.

This is strong evidence that a dedicated Mattermost bridge is practical.

---

# Proposed Architecture

```text
                    Google infrastructure
                           │
                           │ Google Messages
                           │ paired-device protocol
                           │
                 ┌─────────▼─────────┐
                 │   Android phone   │
                 │                   │
                 │ Google Messages   │
                 │ SMS / MMS / RCS   │
                 └─────────┬─────────┘
                           │
                           │ paired session
                           │
                 ┌─────────▼─────────┐
                 │   mm-gmessages    │
                 │                   │
                 │     Go daemon     │
                 │                   │
                 │      libgm        │
                 └─────────┬─────────┘
                           │
                    Mattermost API
                           │
                 ┌─────────▼─────────┐
                 │    Mattermost     │
                 │                   │
                 │   #messages       │
                 │   bot account     │
                 └───────────────────┘
```

The bridge acts as a paired Google Messages client.

It does **not** need to:

- read Android notifications
- scrape the Google Messages UI
- use Tasker or MacroDroid
- run an SMS-specific Android gateway
- maintain direct LAN connectivity to the phone

The Android phone remains the actual SMS/RCS endpoint.

---

# Why This Approach Works for RCS

Google does not provide a public consumer API allowing arbitrary applications to access a user's personal Google Messages/RCS inbox.

The public Google RCS APIs are intended primarily for **RCS Business Messaging**, not personal messaging.

`libgm` instead implements the private protocol used by Google Messages paired clients.

This allows the bridge to function conceptually like:

```text
Google Messages for Web
```

except that the client is a Go daemon instead of a browser.

Consequently, the bridge can handle actual RCS traffic rather than converting messages to SMS.

---

# Mattermost Integration

The bridge should use a dedicated **Mattermost bot account** and Mattermost's REST/WebSocket APIs.

An incoming-only Mattermost webhook would be sufficient for simple notifications, but it would be too limited for a proper bidirectional bridge.

A bot account allows the daemon to:

- create posts
- create threads
- upload attachments
- update posts
- receive Mattermost events
- detect replies
- represent delivery state
- map Mattermost threads to Google Messages conversations

---

# Conversation Model

A clean Mattermost representation is:

```text
#messages

┌─ +1 514 555-1212 ───────────────────
│
│  16:42  Contact
│  I'm leaving now
│
│  16:43  jfgratton
│  OK. Text me when you arrive.
│
│  16:44  Contact
│  [ photo.jpg ]
│
└──────────────────────────────────────
```

Each Google Messages conversation maps to a Mattermost thread.

Core mapping:

```text
Google Messages conversation ID
              ↕
Mattermost root post / thread ID
```

Individual message IDs should also be stored for:

- deduplication
- reactions
- replies
- delivery updates
- edits, if supported
- attachment correlation

---

# Message Flows

## Incoming SMS/RCS

```text
Remote contact
      │
      ▼
Google Messages
      │
      ▼
libgm event
      │
      ▼
mm-gmessages
      │
      ▼
lookup conversation mapping
      │
      ▼
Mattermost thread
```

If no Mattermost thread exists yet, the bridge creates one and stores the mapping.

---

## Reply from Mattermost

```text
Mattermost user
      │
      ▼
reply in mapped thread
      │
      ▼
Mattermost event
      │
      ▼
mm-gmessages
      │
      ▼
lookup Google conversation ID
      │
      ▼
libgm.SendMessage(...)
      │
      ▼
Google Messages / RCS
      │
      ▼
recipient
```

This provides genuine bidirectional RCS messaging.

---

# Media / MMS / RCS Attachments

`libgm` exposes media upload/download functionality.

Incoming media can therefore be:

1. downloaded by the bridge;
2. uploaded to Mattermost;
3. attached to the appropriate Mattermost post.

The reverse flow can similarly upload Mattermost attachments to Google Messages where the underlying protocol supports it.

Conceptually:

```text
Google Messages attachment
        │
        ▼
     libgm
        │
        ▼
   temporary file
        │
        ▼
Mattermost Files API
        │
        ▼
Mattermost post
```

---

# Suggested Go Project Layout

```text
mm-gmessages/
│
├── cmd/
│   └── mm-gmessages/
│       └── main.go
│
├── internal/
│   ├── gmessages/
│   │   ├── client.go
│   │   ├── auth.go
│   │   ├── events.go
│   │   ├── messages.go
│   │   └── media.go
│   │
│   ├── mattermost/
│   │   ├── client.go
│   │   ├── events.go
│   │   ├── posts.go
│   │   └── media.go
│   │
│   ├── bridge/
│   │   ├── conversations.go
│   │   ├── inbound.go
│   │   ├── outbound.go
│   │   └── delivery.go
│   │
│   └── storage/
│       └── sqlite.go
│
├── go.mod
├── go.sum
└── README.md
```

The daemon should remain deliberately small.

There is no obvious need for:

- PostgreSQL
- Redis
- Kafka
- RabbitMQ

SQLite is sufficient for the expected message volume and mapping requirements.

---

# Suggested Persistent State

Minimal SQLite schema:

```sql
CREATE TABLE conversations (
    gmessages_conversation_id TEXT PRIMARY KEY,
    mattermost_root_post_id   TEXT NOT NULL,
    phone_number              TEXT,
    display_name              TEXT,
    last_seen                 DATETIME
);

CREATE TABLE messages (
    gmessages_message_id      TEXT PRIMARY KEY,
    mattermost_post_id        TEXT NOT NULL,
    conversation_id           TEXT NOT NULL,
    direction                 TEXT NOT NULL,
    created_at                DATETIME
);
```

Additional fields can be introduced later for:

- RCS delivery state
- attachment metadata
- reactions
- reply relationships
- sender identity in group conversations

---

# Pairing / Authentication

The daemon needs to pair with Google Messages in the same general manner as Google Messages for Web or another linked device.

Typical initial workflow:

```text
mm-gmessages pair
```

The user then approves/pairs the client from Google Messages on the Android phone.

After pairing, session credentials must be persisted securely so that normal daemon operation does not require repeated interactive authentication.

Possible command structure:

```text
mm-gmessages pair
mm-gmessages daemon
mm-gmessages status
mm-gmessages logout
```

Secrets/session data should be stored with restrictive filesystem permissions and ideally support an external secret store later if desired.

---

# Deployment

A practical deployment would be:

```text
Linux host
│
├── Mattermost
│
└── mm-gmessages
       │
       ├── persistent state
       ├── Google Messages session
       └── SQLite database
```

The Android phone does not need to remain on the same LAN.

It only needs:

- Google Messages
- working SMS/RCS service
- Internet connectivity
- the paired-device relationship to remain valid

---

# Risks and Caveats

## 1. Private / Reverse-Engineered Protocol

This is the largest risk.

`libgm` implements a protocol used internally by Google Messages rather than a stable, officially supported consumer API.

Google can change:

- authentication
- pairing
- protobuf formats
- message semantics
- transport endpoints
- device/session handling

Such changes may temporarily break the bridge until `libgm` is updated.

This should be treated as the primary maintenance risk.

---

## 2. RCS Is Google-Messages Dependent

The bridge is effectively tied to Google Messages' paired-client architecture.

If the Android device stops using Google Messages as its messaging client, the solution may cease to work.

---

## 3. Session Revocation

Google can invalidate or unlink paired-device sessions.

The daemon therefore needs:

- session-health detection
- clear logs
- a `status` command
- a simple re-pairing process

---

## 4. Duplicate Messages

Network reconnects or event replay may produce repeated events.

The bridge should deduplicate using the Google Messages message ID before creating Mattermost posts.

---

## 5. Mattermost Loop Prevention

Messages posted by the bridge itself must never be interpreted as outbound user replies.

The bridge should ignore:

- its own bot user ID
- delivery-status posts
- system-generated posts

---

## 6. Group RCS

Group chats introduce additional metadata:

- multiple senders
- display names
- sender IDs
- group title
- member changes

The storage model should therefore avoid assuming that a conversation maps to exactly one phone number.

---

## 7. Licensing

`mautrix/gmessages` / `libgm` is distributed under **AGPL-3.0**.

For a private self-hosted tool this may be acceptable, but licensing implications should be reviewed before distributing the bridge or offering it as a network service to third parties.

---

# Recommended MVP

Do not begin with full SMS/MMS/RCS parity.

Implement the smallest vertical slice first.

## Phase 1 — Pairing

Goal:

```text
Linux daemon ↔ Google Messages
```

Implement:

- initialize `libgm`
- pair device
- persist session
- reconnect after restart
- list conversations

Success criterion:

The daemon can list existing Google Messages conversations.

---

## Phase 2 — Receive One Message

Implement:

```text
Google Messages event
        │
        ▼
stdout/log
```

Success criterion:

An incoming SMS or RCS message appears in the daemon logs with:

- conversation ID
- message ID
- sender
- message body
- message type

---

## Phase 3 — Mattermost Posting

Add Mattermost bot integration.

Implement:

```text
incoming RCS
    │
    ▼
Mattermost channel/thread
```

Success criterion:

An incoming RCS message appears in Mattermost.

---

## Phase 4 — Mattermost Reply

Listen for Mattermost replies and map the thread back to the Google Messages conversation.

Implement:

```text
Mattermost reply
      │
      ▼
libgm.SendMessage()
      │
      ▼
RCS recipient
```

Success criterion:

A reply typed in Mattermost arrives on the remote phone as an RCS message.

At this point the core project is proven.

---

## Phase 5 — Persistence

Add SQLite mappings:

```text
conversation ID ↔ Mattermost thread
message ID      ↔ Mattermost post
```

Add:

- deduplication
- restart-safe mappings
- reconnect handling

---

## Phase 6 — Media

Implement:

- inbound image attachments
- outbound image attachments
- MIME handling
- Mattermost file uploads
- Google Messages media upload/download

---

## Phase 7 — Message Semantics

Add support for:

- delivery state
- reactions
- replies/quoting
- read state
- typing indicators, if desired
- group conversations

---

# Recommended First Milestone

The first useful proof-of-concept should deliberately do only this:

```text
1. Pair with Google Messages.
2. Receive an RCS message.
3. Post it in Mattermost.
4. Reply in the Mattermost thread.
5. Send that reply back through RCS.
```

If those five operations work reliably, the central technical risk has been retired.

Everything beyond that is incremental engineering.

---

# Final Recommendation

The original SMSGateway/SMSGate design is appropriate only when SMS/MMS is sufficient.

Because **RCS is mandatory**, the preferred solution is:

```text
Google Messages
      ⇅
    libgm
      ⇅
custom Go bridge
      ⇅
Mattermost bot/API
```

This solution is technically viable and aligns well with a small self-hosted Go daemon.

The main trade-off is clear:

> Full SMS/RCS capability is possible, but it depends on a reverse-engineered Google Messages protocol rather than an official long-term-stable consumer API.

That dependency should be accepted explicitly before implementation begins.

Despite that limitation, `libgm` is currently the strongest available foundation for a genuine bidirectional **Google Messages / RCS ↔ Mattermost** gateway.
