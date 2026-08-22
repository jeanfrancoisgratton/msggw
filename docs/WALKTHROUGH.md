# msggw — a guided tour of the code

This is written for someone who knows Go but doesn't yet know *this* codebase, and
wants to actually hold it in their head — not just skim a reference. So instead of
starting with the architecture diagram, this starts with a story: one text message,
followed line by line through the real code, from the moment it lands on the phone
to the moment it shows up in Mattermost. Everything else in this document exists to
support that story.

Read section 1 and 2 first, in order. After that, sections 3 onward are reference
material you can jump into as needed — but they'll make more sense having read the
story first, because you'll already recognise the names.

Line numbers will drift as the code changes. Function and file names will not; use
those to relocate anything that has moved.

---

## Contents

1. [The mental model, in one paragraph](#1-the-mental-model-in-one-paragraph)
2. [The story: one message, start to finish](#2-the-story-one-message-start-to-finish)
   - [2a. Incoming: a text arrives on the phone](#2a-incoming-a-text-arrives-on-the-phone)
   - [2b. Outgoing: you reply from Mattermost](#2b-outgoing-you-reply-from-mattermost)
3. [How the program starts up](#3-how-the-program-starts-up)
4. [The five packages, one at a time](#4-the-five-packages-one-at-a-time)
   - [4a. `internal/config`](#4a-internalconfig)
   - [4b. `internal/secrets`](#4b-internalsecrets)
   - [4c. `internal/storage`](#4c-internalstorage)
   - [4d. `internal/gmessages`](#4d-internalgmessages)
   - [4e. `internal/mattermost`](#4e-internalmattermost)
5. [Go patterns this codebase leans on](#5-go-patterns-this-codebase-leans-on)
6. [What's actually on disk](#6-whats-actually-on-disk)
7. [How concurrent is this, really?](#7-how-concurrent-is-this-really)
8. [Where to go to change something](#8-where-to-go-to-change-something)
9. [Known rough edges](#9-known-rough-edges)

---

## 1. The mental model, in one paragraph

`message-gateway` is one program with two live network connections open at the same time:
one to an Android phone (through Google's own Google Messages protocol), one to a
Mattermost server (as a bot). It sits in a loop reading whatever arrives on either
connection, and when something arrives on one side, it does something on the
other: a text on the phone becomes a Mattermost post, a reply typed in Mattermost
becomes a text sent from the phone. To know *where* in Mattermost a given
conversation belongs, and to avoid re-posting the same message twice after a
reconnect, it keeps a small database of "this phone conversation is this
Mattermost thread" mappings. That's the whole program. Everything else — config
parsing, secret handling, retry loops, the Postgres option — exists in service of
that one loop.

```text
                     ┌───────────────────┐
  Android phone  ⇄   │  message-gateway  │   ⇄  Mattermost server
  (Google Messages)  │  daemon           │      (as a bot account)
                     │  + a database     │
                     │  of mappings      │
                     └───────────────────┘
```

The two sides never talk to each other directly, and — this matters — the code
that speaks to the phone (`internal/gmessages`) and the code that speaks to
Mattermost (`internal/mattermost`) don't import each other and don't know the
other package exists. Only one package, `internal/bridge`, knows about both. If
you ever catch yourself wanting to `import "msggw/internal/mattermost"` from
inside `gmessages`, that's a sign the logic you're writing belongs in `bridge`
instead. This isn't a style preference; it's what keeps the "Google changes their
protocol" problem contained to one package instead of spreading everywhere.

---

## 2. The story: one message, start to finish

Forget the architecture for a moment. Here is a concrete scenario: your daemon is
running, paired, connected. A friend texts you "running late". Here is *every
single thing* that happens, traced through the actual functions, in the order
they run.

### 2a. Incoming: a text arrives on the phone

**Step 1 — libgm notices.**
The library this project is built on, `go.mau.fi/mautrix-gmessages/pkg/libgm`,
maintains a persistent connection to Google's servers (this is the same
mechanism "Google Messages for Web" uses — the daemon behaves like a paired
browser tab that never closes). When your friend's text arrives at Google's
servers, libgm's own background goroutine notices it and calls a callback we
registered.

That callback is `handleLibgmEvent`, in `internal/gmessages/events.go:207`. It's
a big `switch` over every kind of thing libgm can report — a new message, a
renamed conversation, a pairing being revoked, a ping timing out. For a plain
text message, the relevant branch is:

```go
// internal/gmessages/events.go:235
case *libgm.WrappedMessage:
    conv, _ := c.conversation(evt.GetConversationID())
    c.emit(MessageEvent{Message: convertMessage(evt.Message, evt.IsOld, conv)})
```

Two things happen here, and both matter:

- `convertMessage` (`events.go:131`) translates libgm's protobuf-generated
  `*gmproto.Message` — a type this project doesn't control and that could change
  shape if Google tweaks their protocol — into `gmessages.Message`, a plain
  struct this project *does* control (`types.go:207`). From this point on,
  nothing else in the codebase ever touches the raw protobuf type. That
  translation boundary is the single most important design decision here: it's
  what means a Google protocol change only ever requires editing this one file.
- `c.emit(...)` doesn't hand the message to anyone directly. It pushes it onto an
  internal queue and returns immediately. This callback is running *on libgm's
  own goroutine* — the same one responsible for keeping the connection to Google
  alive and answering pings — so it must never block. If it did, and Mattermost
  happened to be slow to accept a post, the phone-side connection would stall
  and Google would eventually consider the session dead.

**Step 2 — the queue hands it off.**
The queue (`events.go:307`, type `queue`) is a small hand-rolled FIFO. Nothing
exotic — a slice plus a `sync.Cond` — but worth understanding because it's an
unusual choice. A Go channel would be the obvious tool here, except a channel
needs a fixed size or it needs someone always ready to receive, and neither is
true here: sometimes Mattermost is briefly unreachable and messages need
somewhere to wait that isn't "blocking libgm's goroutine." So the queue is
unbounded — it just grows a slice — and a separate goroutine drains it. That
goroutine is started by `Client.Events()` (`client.go:142`):

```go
// internal/gmessages/client.go:142
func (c *Client) Events() <-chan Event {
    out := make(chan Event)
    go func() {
        defer close(out)
        for {
            evt, ok := c.events.pop()
            if !ok {
                return
            }
            out <- evt
        }
    }()
    return out
}
```

This is called exactly once, in `daemon.go` indirectly via `bridge.Run` — see
step 3. It turns the queue into an ordinary Go channel that the rest of the
program can `select` on.

**Step 3 — the bridge's main loop picks it up.**
`Bridge.Run` (`internal/bridge/bridge.go:77`) is the heart of the whole daemon.
It is one `for { select { ... } }` loop, and it is the *only* place where events
from either side are actually handled:

```go
// internal/bridge/bridge.go:84 (trimmed)
for {
    select {
    case <-ctx.Done():
        return nil
    case <-prune.C:
        b.prunePending(ctx)
    case evt, ok := <-gmEvents:
        b.handleGMEvent(ctx, evt)
    case evt, ok := <-mmEvents:
        b.handleMMEvent(ctx, evt)
    }
}
```

`select` waits until one of these channels has something ready, then runs that
one case. Our `MessageEvent` arrives on `gmEvents`, so `handleGMEvent` runs,
which for a `MessageEvent` calls `handleIncomingMessage` (`bridge.go:125` routes
to `inbound.go:24`). This is the function that actually decides what to do with
the message — and it has more work to do than you'd expect, because the exact
same event type (`MessageEvent`) is used for three completely different things,
and the first job is telling them apart.

**Step 4 — is this actually new, or something else in disguise?**
Open `internal/bridge/inbound.go:24`, `handleIncomingMessage`. It runs four
checks, in this order, and the order is the logic:

```go
// internal/bridge/inbound.go:27 (the first check)
if postID, err := b.db.TakePendingOutbound(ctx, msg.TmpID); err == nil {
    // ... this is the echo of a message WE sent — see section 2b
}
```

*Check 1: is this the echo of our own outgoing message?* When Mattermost sends a
message out through the phone, Google Messages doesn't hand back a final message
ID right away — it comes back later as an ordinary incoming-looking `MessageEvent`
carrying a temporary ID (`TmpID`) the daemon chose when it sent. So every
incoming message is first checked against a small table of "things we're
waiting to hear back about." Your friend's message obviously isn't one of ours,
so this check fails and falls through — but keep it in mind, because it's the
missing half of the story in section 2b.

*Check 2: have we already posted this message?* (`inbound.go:46`) — looks the
message ID up in the `messages` table. A reconnect can cause libgm to replay
events it already delivered once; without this check, a replay would post the
same text to Mattermost twice. First time seeing this ID, so: not found, fall
through.

*Check 3: is this a stale replay from before the bridge existed?* (`inbound.go:52`)
— `msg.IsOld` is a flag libgm sets when it's catching you up on history after a
reconnect, rather than reporting something that just happened. Not our case:
this text really did just arrive.

*Check 4: does the message actually have content?* (`inbound.go:63`) — some
events are pure status updates (delivered, read) with no text or media at all;
posting an empty message would be pointless. Ours has text, so this passes too.

Having survived all four checks, this is genuinely a new, real message. Now the
real work starts.

**Step 5 — find or create the Mattermost thread.**
`ensureConversation` (`internal/bridge/conversations.go:26`) is called with the
conversation ID. It first checks the database (`b.db.GetConversation`) for an
existing mapping. The very first message from your friend, there won't be one
yet, so it does the "create" path:

```go
// internal/bridge/conversations.go:38 (trimmed)
conv, err := b.gm.GetConversation(ctx, conversationID)
destination, rule := b.router.Route(conv)
channelID, err := b.mm.ResolveDestination(ctx, destination)
```

`b.router.Route` (`internal/bridge/routing.go:85`) is where your `routing.rules`
config actually gets applied — matched against phone number, conversation ID, or
a name pattern, first match wins, falling back to `routing.default` if nothing
matches. This is pure config-driven logic; nothing about *where* things land in
Mattermost is hard-coded anywhere else in the program. Once a destination (a
channel, a DM, a group DM) is decided, `mm.ResolveDestination` turns that
abstract destination into an actual Mattermost channel ID, making REST calls to
look it up (and joining the bot to the channel if `routing.join_channels` allows
it).

If `routing.thread_per_conversation` is on (the default), a root post is created
right now to open the thread:

```go
// internal/bridge/conversations.go:62
rootID, err := b.mm.Post(ctx, mattermost.NewPost{
    ChannelID: channelID,
    Message:   conversationHeader(conv),
})
```

That header post is the `#### Friend Name` / `_RCS conversation with +1 514
555-1212_` line you'd see at the top of a bridged thread. The mapping
(conversation ID ↔ channel ID ↔ root post ID) is then written to the database
(`b.db.SaveConversation`) so the next message in this conversation skips
straight to the lookup and doesn't repeat any of this.

**Step 6 — post the actual message.**
Back in `handleIncomingMessage`, with the conversation resolved, `postMessage`
(`inbound.go:96`) builds and sends the Mattermost post:

```go
// internal/bridge/inbound.go:97 (trimmed)
fileIDs, notes := b.transferAttachments(ctx, conv, msg)
post := mattermost.NewPost{
    ChannelID: conv.ChannelID,
    RootID:    conv.RootPostID,
    Message:   formatMessage(conv, msg, notes),
    FileIDs:   fileIDs,
}
postID, err := b.mm.Post(ctx, post)
```

`formatMessage` (`inbound.go:193`) is where the text `"running late"` actually
turns into the markdown you'd see in Mattermost — something like:

```
**Friend Name** _RCS_
running late
```

The sender's name is written into every single post — deliberately, because a
Mattermost thread only ever shows the *bot* as the author of every post; without
naming the human sender inline, a group chat would be unreadable. The `_RCS_` /
`_SMS_` label is there because "did this actually travel over RCS or quietly
fall back to SMS" is the whole reason this project exists — it's meant to be the
first thing you notice, not something you have to dig for.

If your friend had sent a photo, `transferAttachments` (`inbound.go:145`) would
have run first: download it from Google Messages (`b.gm.Download`), re-upload it
to Mattermost (`b.mm.Upload`), and collect the resulting file ID. Notably, a
failed attachment transfer does **not** stop the message from posting — the text
is worth having even if the photo couldn't be fetched, so a failure there just
adds an italic note to the post instead of aborting.

**Step 7 — remember it happened.**
The very last thing `postMessage` does is write a row into the `messages` table
mapping the Google Messages message ID to the Mattermost post ID it just
created. This is the row that check 2 in step 4 will find if this exact message
event is ever replayed — it's the entire deduplication mechanism, and it's one
`INSERT` wide.

That's it. From "friend hits send" to "text visible in Mattermost" is: libgm
callback → queue → channel → `Bridge.Run`'s select → `handleIncomingMessage`'s
four disambiguating checks → `ensureConversation` (routing + channel resolution
+ thread creation) → `postMessage` (attachments + markdown + the actual REST
call) → a row in `messages` so it never happens again for this ID.

### 2b. Outgoing: you reply from Mattermost

Now the reverse trip. You type "OK, no rush" as a reply in that thread.

**Step 1 — Mattermost's WebSocket reports the post.**
`mattermost.Client.Listen` (`internal/mattermost/events.go:78`) holds an open
WebSocket to the Mattermost server and reconnects it automatically if it drops
(with a backoff that doubles up to one minute — `nextBackoff`, `events.go:242`).
When your reply posts, the server pushes a `posted` event down that socket,
which `pump` (`events.go:128`) reads and hands to `decodePost` (`events.go:168`).

`decodePost` is where **loop prevention** lives, and it's worth pausing on
because getting it wrong is how a bridge like this ends up in an infinite
message war with itself:

```go
// internal/mattermost/events.go:186
switch {
case post.UserID == c.BotUserID():
    return Post{}, false   // our own post, echoed back by the server
case post.FromBridge:
    return Post{}, false   // belt-and-braces: the bridge's own marker
case post.Type != "":
    return Post{}, false   // a system post — "user joined the channel"
}
```

The first message this daemon posted in step 2a — the one with "running late"
in it — comes back down this exact same WebSocket, because Mattermost tells
every connected client about every post, including the bot's own. If that post
weren't filtered out right here, the bridge would try to send its own message
back out through the phone. This filtering deliberately happens in the
`mattermost` package rather than in `bridge`, precisely because a post that
slips past this point goes straight to `SendReply` and out onto a real carrier
network — there's no second safety net downstream.

Your actual reply — from a real person, not the bot — passes all three checks
and gets emitted as a `PostEvent`.

**Step 2 — the bridge picks it up.**
Same `Bridge.Run` loop from section 2a, except this time the event lands on
`mmEvents` instead of `gmEvents`, and `handleMMEvent` routes it to
`handleOutgoingPost` (`internal/bridge/outbound.go:23`).

**Step 3 — which conversation is this a reply to?**
`conversationForPost` (`outbound.go:80`) has to answer this from the *post*
alone. In thread mode (the default), it's simple: every post in this thread has
`RootID` set to the conversation's root post, so
`b.db.GetConversationByRootPost` finds the mapping directly. (Without threading,
it falls back to "is there exactly one conversation bridged into this channel,"
and deliberately refuses to guess if there's more than one — guessing wrong here
means sending a private reply to the wrong person, which is worse than any
error message.)

If the post isn't inside any bridged thread at all — someone just chatting in
the channel, not replying to a text — `ErrNotFound` comes back, and that's
treated as completely normal, not an error: people do talk in these channels
outside the bridged threads.

**Step 4 — is this a reply to one specific message, or just to the thread?**
`replyTarget` (`outbound.go:105`) exists because RCS supports quoted replies
("replying to: ..."), and Mattermost threading doesn't map onto that cleanly. In
thread mode, *every* post in the thread technically has `RootID` set to the same
root post — so that alone tells you nothing about which specific message is
being answered. Only when someone explicitly replies to a message that is
*itself* a reply (Mattermost then sets `RootID` to that message, not the thread
root) does the code know to look up a specific Google Messages message ID to
quote.

**Step 5 — actually send it.**
```go
// internal/bridge/outbound.go:57
result, err = b.gm.SendReply(ctx, conv.ID, replyToID, text)
```

This calls down into `gmessages.Client.send` (`internal/gmessages/messages.go:125`),
which builds the actual protobuf request — picking the right SIM card via
`simPayload` (`client.go:269`, relevant on dual-SIM phones), generating a fresh
`tmpID` with `uuid.NewString()`, and deciding whether to set `ForceRCS` (only
when the conversation is already RCS *and* not latched to SMS-only mode —
forcing it elsewhere makes the phone reject the send outright rather than
falling back gracefully).

**Step 6 — remember what we just sent, so its echo doesn't get re-posted.**
```go
// internal/bridge/outbound.go:67
b.db.AddPendingOutbound(ctx, result.TmpID, post.ID, conv.ID)
```

This is the row that check 1 in section 2a, step 4 (`TakePendingOutbound`) will
find. When Google Messages echoes this exact message back a moment later —
which it always does, because that's how it hands you the *real* message ID —
the bridge recognises it as its own, links the real message ID to the
Mattermost post you already see, and does **not** post it a second time. Miss
this step, and every reply you type would appear to double-post itself a moment
later.

There's a cleanup detail worth knowing: `prunePending` (`bridge.go:191`), fired
every 15 minutes by the ticker in `Bridge.Run`, deletes any pending-outbound row
older than an hour. If the phone never acknowledges a send at all, that row
would otherwise sit there forever.

**If the send fails** — no signal, Google Messages isn't the default SMS app,
whatever — `reportSendFailure` (`outbound.go:154`) posts the error message
*back into the same thread you typed in*, rather than only logging it. The
reasoning stated right in the code is worth repeating: a failure that only shows
up in the daemon's log is, from your point of view, a message you believe was
delivered. Silently swallowing send failures would be actively misleading.

And that's the round trip. Every other feature in the codebase — media
transfer, delivery status reactions, group chat participant tracking, backfill
— is a variation or an extension of these same two flows. If you understand
these two functions, `handleIncomingMessage` and `handleOutgoingPost`, you
understand the program.

---

## 3. How the program starts up

Now that you've seen what runs in steady state, here's how it gets there.
`src/main.go` is nine lines and just calls `cmd.Execute()`, which is
[Cobra](https://github.com/spf13/cobra)'s entry point — `message-gateway` is a
standard "one root command, several subcommands" CLI (`config`, `pair`, `daemon`,
`status`, `logout`). Every subcommand shares one flag, `--config`
(`cmd/root.go:51`), and is registered in `init()` (`root.go:47`) — Go runs
`init()` automatically before `main`, which is how Cobra's whole command tree
gets assembled without a central list.

The `daemon` command (`cmd/daemon.go:20`) is the one that runs the loop from
section 2. Read it top to bottom; it's the canonical wiring sequence:

```go
// cmd/daemon.go:30 (trimmed to the sequence)
cfg, err := loadConfig()
log := newLogger(cfg)
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
db, err := openStorage(ctx, cfg)          // sqlite, or postgres — see 4c
mm, err := newMattermost(ctx, cfg, log)   // connects + authenticates now
gm, err := gmessages.New(gmCfg)
gm.Connect(ctx)                            // brings up the phone session
br, err := bridge.New(cfg, log, db, gm, mm)
return br.Run(ctx)                         // the loop from section 2
```

Everything gets built and connected up front, in `cmd/common.go` (the
"composition root" — every subcommand that needs a Mattermost client, a storage
handle, or a Google Messages client builds it the same way, through
`newMattermost`, `openStorage`, `newGMessagesConfig`). `bridge.New` receives all
of it already wired and connected; the bridge itself does no construction, which
is what makes it plausible to unit-test in isolation from real network
connections.

One line worth noticing: `signal.NotifyContext` (`daemon.go:38`) turns Ctrl-C
and `SIGTERM` into a cancelled `context.Context`. Everything downstream that
takes a `ctx` — the bridge's `select` loop, the WebSocket reconnect loop, the
long-polling libgm connection — unblocks the moment that context is cancelled.
That's the entire shutdown mechanism: one signal, one cancelled context,
everything downstream notices on its own. There's no manual "tell every
goroutine to stop" bookkeeping anywhere.

The shutdown sequence itself is a `defer` chain, and the order those `defer`s
were *registered* in is the reverse of the order they *run* in (Go's defers are
last-in-first-out):

```go
// cmd/daemon.go, condensed
db, err := openStorage(ctx, cfg)
defer db.Close()                     // registered 1st → runs LAST
...
defer func() {
    gm.Disconnect()
    gm.SaveSession()                 // registered 2nd → runs 2nd-to-last
}()
```

So on shutdown, the Google Messages session gets saved *before* the database
closes — which matters, because saving it might still touch things that need
the database to be alive a moment longer. Ordering your `defer`s is ordering
your shutdown sequence, in reverse.

---

## 4. The five packages, one at a time

You've now seen `bridge`, `gmessages`, and `mattermost` doing real work in
section 2, and `cmd` wiring them together in section 3. The remaining two —
`config` and `secrets` — are what feeds all of the above their settings and
credentials. Here's each package's job, its one or two genuinely important
design decisions, and nothing else.

### 4a. `internal/config`

Reads `/etc/msggw/config.json`, decodes it into `Config`
(`internal/config/types.go:37`), fills in defaults, and validates it —
that's the whole job, stated explicitly in the package doc comment
(`config.go:6`):

> Nothing in here reaches out to Vault, Mattermost or Google: `Load` only
> proves that the configuration is internally coherent. Credentials are
> resolved, and therefore fail, at the point they are used.

That separation is what makes `message-gateway config check` a meaningful *offline*
command — it can tell you your JSON is well-formed and your routing rules make
sense without ever touching the network. Whether your Mattermost token
actually works is a separate concern, checked separately.

Two details worth knowing:

**Unknown fields are rejected, on purpose:**
```go
// config.go:57
dec.DisallowUnknownFields()
```
Without this, misspelling `"state_dir"` as `"statedir"` would silently be
ignored and the daemon would quietly run with a default nobody chose. With it,
that typo is a startup error instead of a mystery three weeks later.

**One field can't use `bool`'s zero value, because "unset" and "false" are both
meaningful:**
```go
// types.go:134
ThreadPerConversation *bool `json:"thread_per_conversation,omitempty"`
```
The default for this setting is `true`. A plain `bool` can't tell "the operator
explicitly wrote `false`" apart from "the operator didn't mention this field at
all" — both would just read as the zero value, `false`. Using a pointer lets
`nil` mean "not set, so apply the default," which is what `applyDefaults`
(`config.go:113`) does. The cost is that every *read* of the setting has to go
through a small accessor, `ThreadPerConversationEnabled()` (`config.go:254`),
instead of reading the field directly — that's the standard Go answer to
tri-state JSON booleans, and you'll recognise the pattern if you see it
elsewhere.

The storage-backend fields live here too, as a `BackendConfig` (`types.go:59`)
holding `Driver` plus one nested block per backend, `SQLite` (`types.go:71`)
and `Postgres` (`types.go:78`). Both blocks can be filled in at once — the
sample ships both — and only the one `Driver` names is ever read; `Validate`
(`config.go:124`) only checks that the *active* block is complete (e.g.
`backend.postgres.dsn_ref` is required once `backend.driver` is `postgres`),
not that the inactive one is empty.

### 4b. `internal/secrets`

Every credential in the config file — the Mattermost bot token, the Google
Messages session, the Postgres DSN — is written as a *reference*, never a raw
value: `env:MM_BOT_TOKEN`, `file:/etc/msggw/mm.token`, `vault:secrets/msggw#bot_token`,
and so on. `secrets.Open` (`secrets.go:43`) parses the scheme prefix and returns
a `Store` — an interface with exactly three methods:

```go
// types.go:31
type Store interface {
    Load() ([]byte, error)
    Save(value []byte) error
    Describe() string
}
```

Five small unexported types implement this (`envStore`, `fileStore`,
`encodedFileStore`, `vaultStore`, `literalStore`, all in `stores.go`) — and
none of them mention the word `Store` anywhere in their own code. That's how Go
interfaces work: a type satisfies an interface just by having the right
methods, with no `implements` keyword and no declared relationship. The payoff
is that `bridge` and `gmessages` never know or care which of the five they got
— the code that reads the Google Messages session is identical whether that
session lives in a plain file or in Vault.

Why does the daemon need to be able to *write* these back, not just read them?
Because the Google Messages session isn't a static credential — libgm refreshes
its auth token roughly every hour while running, and the refreshed token has to
be persisted or every restart re-triggers a full QR-code re-pairing. That's why
`config.Validate` specifically rejects `env:` and `literal:` for the session
reference (`config.go:140`): neither can be written back to, and a session that
silently can't survive a restart is worse than an error at startup.

One sharp edge worth knowing if you ever touch `encoded:`: it's AES-256-CFB
with **no integrity check**. A wrong passphrase decodes to plausible-looking
garbage instead of raising an error — there's no way for the encoding itself to
tell you it's wrong. The only reason this is safe in practice is that whatever
reads the result (the session store parsing it as JSON) will notice it's
garbage downstream. If you ever add a new consumer of an `encoded:` secret, it
has to validate what comes back, or it will happily run on noise.

### 4c. `internal/storage`

This is the package with the newest addition: you can now pick **SQLite**
(the default, and the only one actually run against real traffic so far) or
**PostgreSQL** (for running the state store on its own server). The split is
`db.go` (shared logic) + `sqlite.go` + `postgres.go` (one file per backend).

The trick that makes one set of query code work against two different SQL
dialects is placeholder rewriting. Every query anywhere in `conversations.go`
and `messages.go` is written with SQLite-style `?` placeholders:

```go
// storage/conversations.go:73 (excerpt)
INSERT INTO conversations (...) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
```

But PostgreSQL doesn't accept `?` — it wants numbered placeholders, `$1, $2, $3`.
Rather than write every query twice, `DB.rebind` (`db.go:60`) walks the query
string and rewrites `?` into `$1`, `$2`, ... on the fly, but only when the
backend is Postgres:

```go
// db.go:59
func (db *DB) rebind(query string) string {
    if db.backend != BackendPostgres {
        return query   // SQLite takes "?" natively — nothing to do
    }
    // ... walk the string, replacing "?" with "$1", "$2", ...
}
```

Every query goes through `db.execContext` / `queryContext` / `queryRowContext`
(or the `Tx` equivalents), which call `rebind` first. That's the entirety of
the abstraction — no ORM, no query builder, just one small string transform
sitting between "the query as written" and "the query as sent."

Migrations are tracked differently on each backend, because SQLite and Postgres
don't offer the same tools: SQLite has a built-in per-file counter
(`PRAGMA user_version`), so `sqlite.go`'s `migrateSQLite` just reads and bumps
that. Postgres has no equivalent, so `postgres.go`'s `migratePostgres` creates
its own `schema_migrations` table and tracks the version there instead. Both
apply the same rule, though, stated in the comment above `sqliteMigrations`:
**migration entries are never edited once released, only appended to.** Editing
an already-shipped migration does nothing for a database that already ran it,
and quietly diverges the schema between old and new installs — append a new
one instead.

The actual schema (identical on both backends) is four tables:

- **`conversations`** — one row per phone conversation: which Mattermost
  channel and root post it maps to, its display name, and
  `outgoing_participant_id` (needed per-conversation because a dual-SIM phone
  sends as a different identity depending on which SIM a conversation uses).
- **`conversation_participants`** — a separate table, not a column, because a
  group RCS chat has several phone numbers in it, and a contact's number can
  even change mid-relationship. `normalized_phone` (punctuation stripped) is
  the actual match key `NormalizePhone` (`conversations.go:50`) computes; the
  router (section 2a, step 5) and the storage layer both call this same
  function, so `+1 (514) 555-1212` and `+15145551212` are guaranteed to compare
  equal everywhere, not just in one place.
- **`messages`** — the deduplication table from section 2a, step 4/check 2.
  `gmessages_message_id` is the primary key; that uniqueness constraint is the
  entire mechanism preventing a replayed event from posting twice.
- **`outbound_pending`** — the `tmp_id → post_id` table from section 2b,
  step 6.

### 4d. `internal/gmessages`

You've already read most of the important code in this package in section 2 —
`handleLibgmEvent`, the queue, `convertMessage`, `send`. Two things weren't
covered there.

**The `Status` type is a small int with a lot of methods hung off it**
(`types.go:51`):
```go
type Status int32
func (s Status) Outgoing() bool  { ... }
func (s Status) Delivered() bool { ... }
func (s Status) Read() bool      { ... }
func (s Status) Failed() bool    { ... }
func (s Status) Pending() bool   { ... }
```
`Status` is really just `gmproto.MessageStatusType` (a generated enum) wearing
a local name, with predicate methods added. `Failed()` alone checks against
thirteen distinct protocol-level failure states — because from Mattermost's
point of view, "recipient lost RCS" and "message too large" and "failed to
encrypt" all mean exactly one thing: this didn't arrive, show a warning. Defining
a named type over a primitive and hanging methods on it is a very ordinary Go
move, and it's how this codebase avoids thirteen `switch` statements scattered
across every file that cares whether a message failed.

**The `Event` interface is a closed set, on purpose** (`events.go:21`):
```go
type Event interface{ isGMEvent() }
func (ReadyEvent) isGMEvent()   {}
func (MessageEvent) isGMEvent() {}
// ... one per event type, all in this file
```
`isGMEvent` is a method nobody ever calls directly — its only job is to exist.
Because it's unexported, no type declared *outside* this package can implement
it, which means no type outside `gmessages` can ever satisfy `Event`. That's
Go's usual substitute for what other languages call a sum type or tagged union:
you get a fixed, enumerable set of "the kinds of thing this can be," and the
compiler (via `switch e := evt.(type) { case ReadyEvent: ... }`, as seen in
`bridge.go:118`) forces you to handle each one explicitly.

### 4e. `internal/mattermost`

Also mostly covered already — `Listen`, `decodePost`'s loop guard,
`ResolveDestination`. One thing worth adding: the caching in `channels.go`.
Resolving a `Destination` (say, "the `#messages` channel in team `myteam`")
into an actual Mattermost channel ID costs up to three REST calls — look up the
team, look up the channel in it, check membership. That's fine to pay once, but
paying it on *every single message* would put a noticeable REST round-trip in
front of every text your friend sends. So the resolution is cached in a plain
map, keyed by every field that could distinguish two different destinations
from each other:

```go
// channels.go:160
func destinationKey(dest config.Destination) string {
    return strings.Join([]string{
        dest.Type, dest.Team, dest.Channel, dest.ChannelID, dest.User,
        strings.Join(dest.Users, ","),
    }, "\x00")
}
```

The separator is a NUL byte (`\x00`) rather than something printable like `/`
or `-` — deliberately, because a printable separator could let two genuinely
different destinations collide into the same cache key (imagine a channel
literally named `foo/bar` colliding with team `foo`, channel `bar`). A NUL byte
essentially never appears in a Mattermost team or channel name, so it's a safe
join character here.

---

## 5. Go patterns this codebase leans on

You've now seen every one of these in real context above; this section just
names the pattern explicitly and points back to where you already saw it, in
case the name is unfamiliar.

**Errors are values you check, and you can wrap and unwrap them.** No
exceptions in Go — a function that can fail returns `(result, error)`, and
`fmt.Errorf("...: %w", err)` *wraps* the original error so a caller further up
can still identify it. You saw this pattern's most important use in section 2a,
step 4: `errors.Is(err, storage.ErrNotFound)` is how the code tells "nothing is
mapped yet, which is fine" apart from "the database call itself broke," which
is not fine. Getting that distinction wrong in a message bridge is exactly how
you end up posting things twice.

**`context.Context` is the first parameter almost everywhere**, and it's how
one Ctrl-C shuts down everything downstream — see section 3's discussion of
`signal.NotifyContext`.

**`defer` runs on the way out, in reverse order of registration** — see the
shutdown sequence in section 3.

**Interfaces are satisfied implicitly, just by having the right methods** — no
`implements` keyword anywhere. Section 4b's `secrets.Store` is the clearest
example: five types satisfy it without ever mentioning its name.

**A method with a name nobody calls, used to close a type set** — section 4d's
`isGMEvent()` / `isMMEvent()` pattern. If you see a lowercase method that's
never invoked anywhere, this is almost certainly why.

**Zero values are usually fine as-is; a pointer means "this needed to
distinguish absent from zero."** Section 4a's `*bool` for
`ThreadPerConversation` is the one place in this codebase that needs the
distinction badly enough to pay for a pointer.

**`sync.RWMutex` where reads vastly outnumber writes.** Both `gmessages.Client`
and `mattermost.Client` cache things behind an `RWMutex` rather than a plain
`Mutex` — `RLock` lets many goroutines read the cache at once; only a write
needs exclusive access. You'll see this in `client.go` in both packages.

**One genuinely unusual one: `sync.Cond` in the event queue** (section 2a, step
2). This is rare in ordinary Go code, and it's worth understanding the `for`
loop instead of `if` around the wait:

```go
// gmessages/events.go:335
for len(q.items) == 0 && !q.closed {
    q.cond.Wait()
}
```
`Wait()` can return even when nothing actually changed (a "spurious wakeup"),
so the condition has to be rechecked in a loop, not assumed true just because
`Wait()` returned. If you ever reach for `sync.Cond` yourself, copy this shape.

---

## 6. What's actually on disk

| Path | What's there | Written by |
|---|---|---|
| `/etc/msggw/config.json` | your configuration | you |
| `/var/lib/msggw/msggw.db` (SQLite mode) | the mapping database | `internal/storage` |
| a Postgres database (Postgres mode) | the same mapping tables | `internal/storage` |
| wherever `gmessages.session_ref` points | the Google Messages session | `SessionStore` (`gmessages/auth.go`) |
| wherever `mattermost.token_ref` points | your bot's access token | you |

The session file is the one that actually matters day to day: lose it and you
have to re-pair from a QR code. It gets rewritten on every auth-token refresh
(roughly hourly while the daemon runs — section 2's step 1 callback), on
pairing, and on clean shutdown (section 3's defer chain).

One easy-to-miss detail: `message-gateway logout --local-only` says the session "has
been deleted," but under the hood `SessionStore.Clear` calls `store.Save(nil)`
— it truncates the file to zero bytes rather than removing it from disk. The
*behaviour* is correct (an empty file reads back as "not paired," the same as a
missing one), but if you go looking for a deleted file afterwards, you won't
find one — you'll find an empty one.

---

## 7. How concurrent is this, really?

Less than it might look, and that's a deliberate, useful simplification to
understand.

There are exactly three places anything runs concurrently:

1. **libgm's own goroutine**, which calls `handleLibgmEvent` (section 2a, step
   1). It only translates and pushes onto the queue — nothing more — so it's
   always free to get back to its real job of keeping the phone connection
   alive.
2. **The pump goroutines** that drain each side's queue into a Go channel:
   `gmessages.Client.Events()` and `mattermost.Client.Listen()`.
3. Everything else — **`Bridge.Run` itself is a single goroutine, running one
   `for { select {...} }` loop, handling one event completely before starting
   the next.** This is the part that's easy to assume otherwise, so it's worth
   stating plainly: nothing in `handleIncomingMessage` or `handleOutgoingPost`
   runs concurrently with anything else in the bridge. One message is fully
   processed — routed, posted, saved — before the next one is even looked at.

Two consequences follow directly from that:

- The per-conversation lock you'll notice in `bridge.go:53`
  (`conversationLocks`) is currently **not protecting against anything real**.
  Nothing runs concurrently for it to serialise against. It's there as
  scaffolding for a possible future where handlers get their own goroutines —
  reasonable to have in place, but don't read its presence as evidence that
  concurrent message handling already works, because it doesn't yet.
- A slow attachment transfer — a large video downloading from Google and
  re-uploading to Mattermost — **blocks the entire bridge in both directions**
  for as long as it takes. Nothing else gets processed meanwhile: not the next
  incoming text, not your next reply. This is fine at the scale this project is
  built for (one person's messages), and it's the first thing you'd need to
  revisit if throughput ever became a real concern.

If you ever do add real concurrency here, `go build -race ./...` and
`go test -race ./...` will catch the mistakes. Use them from the start, not
after something goes wrong.

---

## 8. Where to go to change something

| You want to... | Start here |
|---|---|
| Add a CLI command | new file in `src/cmd/`, register it in `root.go:47` |
| Add a config field | `config/types.go`, then `applyDefaults` + `Validate` in `config.go`, then update `config.sample.json` |
| Add a secret backend (a new `scheme:`) | implement `Store` in `secrets/stores.go`, add the case in `secrets.go:52` |
| Change how a message looks when posted | `bridge/inbound.go:193`, `formatMessage` |
| Change the thread header text | `bridge/conversations.go:165`, `conversationHeader` |
| Add a new routing criterion | `config/types.go` (`Rule`), then `bridge/routing.go` (compile it, then check it in `matches`) |
| Handle a new kind of phone event | `gmessages/events.go:207`, add an `Event` type in the same file, then a case in `bridge/bridge.go:118` |
| Change delivery-status reactions | `bridge/delivery.go` |
| Add a database table or column | **append** to `sqliteMigrations` and `postgresMigrations` — never edit an existing entry, in either file |
| Change how a Mattermost post is made | `mattermost/posts.go` |

The general shape of a change that touches the whole stack, start to finish:
add the config field → validate it → thread it through `cmd/common.go` so
whichever client needs it gets it at construction → use it in the relevant
protocol package (`gmessages` or `mattermost`) → wire the behaviour into
`bridge`. `GMessages.MarkReadOnBridge` is a small, complete example of this
whole path if you want to trace one end to end: declared in
`config/types.go:93`, consumed in `bridge/inbound.go:84`. That's the entire
round trip for one setting.

---

## 9. Known rough edges

Honest notes from reading the current tree, not bugs that are actively causing
problems today.

**Some exported functions have no callers yet.** `gmessages.Client.SendText`,
`.RequestFullSize`, `.StartConversation`; `mattermost.Client.ChannelIsDirect`,
`.API()`; `storage.DB.GetMeta`, `.SetMeta`, `.FindConversationByPhone`. These
are groundwork for features not built yet — starting a new conversation from
Mattermost, reusing a thread when a contact's number changes, requesting a
full-size MMS instead of a thumbnail. Worth knowing they're untested by actual
use; `FindConversationByPhone` has a unit test, the rest don't.

`SendText` specifically is dead because `handleOutgoingPost` always calls
`SendReply` instead (`outbound.go:57`), which sends without a reply payload
when `replyToID` is simply empty — the two functions differ only in that one
argument, so `SendText` never gets called in practice.

**A stale comment.** `outbound.go:123` says "there is no reverse index because
this path is rare." There is one: `CREATE INDEX messages_by_post ON
messages(mattermost_post_id)` exists in both `sqlite.go` and `postgres.go`. The
lookup this comment is describing is indexed; the comment is just wrong and
could be deleted.

**`Config.NewLogger` takes `*os.File` instead of the more general
`io.Writer`.** Because of that, `cmd/status.go` can't hand it a discarding
writer for a "quiet" mode and has to hand-roll its own instead. Widening the
parameter to `io.Writer` would be a small, safe improvement and a reasonable
first thing to practice a change on.

**The per-conversation lock map only grows.** `conversationLocks`
(`bridge.go:53`) never removes an entry once created. Bounded by the number of
distinct conversations you'll ever bridge, so it's not a real leak at the scale
this runs at — but it is unbounded in principle, and would be worth revisiting
if it ever needs to.

**`NewUnpaired` skips a nil-check that `New` has.** `New` checks that
`cfg.Session` isn't nil (`client.go:70`); `NewUnpaired` (`client.go:98`)
doesn't, so a caller that skipped setting `Session` would nil-dereference
inside `saveSession` later. In practice the only caller is the pairing flow
(`gmessages/pair.go`), which always supplies a session — so this is latent, not
currently reachable, but worth knowing if you ever add a second caller.
