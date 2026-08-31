# Multi-tenancy — a parked design conversation, now implemented

**Status: done.** Single daemon, multiple tenants (option B below) is built:
config is a `users: []` array, `internal/storage` and `internal/bridge` are
threaded with a `tenant string`, `pair`/`logout`/`status` take a per-user
`NAME`, group-chat channels can be auto-created, and each user runs its own
goroutine independently of the others — see [`docs/CONFIGURATION.md#users`](CONFIGURATION.md#users)
for the operator-facing shape. This document is kept as the record of the
conversation and the reasoning that led there; sections below describing
work as "not started" or "not scheduled" are the historical plan, not the
current state.

---

## The idea

Today msggw is single-tenant: one config file, one `gmessages.session_ref`,
one paired phone, one Bridge loop. The question was whether an operator could
instead let several people each pair their own phone against the same
Mattermost server — effectively running msggw as a small shared service
rather than a personal tool.

## Two shapes this could take

**A. One msggw instance per person (no code change).** Separate container,
separate `config.json`, separate `session_ref`, separate `state_dir`/database,
all pointed at the same Mattermost server. Complete isolation, zero new code —
the cost is purely operational (N containers, N configs to hand out, N bot
accounts or one shared one — see below).

**B. One daemon, many tenants — the chosen direction.** A single process
holds several paired sessions at once and bridges all of them. The rest of
this document is about this option.

## What a single multi-tenant daemon would need

Corrections to the original list this conversation started from:

- **Pairing aware of a tenant** — right. The original "templated
  `session_ref`" idea was rejected at the time in favor of each user entry
  writing its own literal `session_ref`, on the reasoning that no templating
  was needed at all. That held until a real deployment showed the failure
  mode this document didn't anticipate: the single-tenant-era config kept
  its one hand-written `session_ref` untouched when a second user was
  added, with nothing forcing a distinct path per tenant — a silent session
  collision waiting to happen, not a hypothetical one. `session_ref` is no
  longer an operator-settable field: a top-level `root_dir` plus
  `Config.SessionRef(user)` derive `encoded:$root_dir/gmessages/$name_session.enc`
  deterministically, so there is no longer a hand-written value that can go
  stale or collide — see [`docs/CONFIGURATION.md#gmessages`](CONFIGURATION.md#gmessages).
  `msg-gw pair NAME` looks NAME up in `users` and pairs into that derived
  reference directly.
- **A bot account per user** — this was a mis-speak in the original ask (the
  first draft said "webhook"), corrected to "bot account." Bot-per-user isn't
  actually necessary: the architecture already deliberately uses a bot account
  over REST+WebSocket rather than a webhook (a webhook can't read replies,
  upload files, or edit posts — see [SOLUTION.md](SOLUTION.md)). **One shared
  bot account, with per-user routing** (each person's conversations default to
  their own Mattermost DM with that bot) covers the same need with far less
  new surface — no per-user Mattermost administration required.
- **Per-user channel existence check** — true only for the group-chat case.
  DMs need nothing new: a Mattermost DM channel is created implicitly on first
  message. **Done:** `routing.join_channels` now also creates a named channel
  that does not exist yet (as private — this is personal message content),
  via `internal/mattermost/channels.go`'s `createChannel`, instead of only
  joining an existing one.
- **Config shape** — global settings (`mattermost`, `log`, `backend`,
  `vault`, `listener`) stay as they are; `users: []` holds, per entry, what
  `gmessages` + `routing` held at the top level before. Implemented as a
  clean break rather than keeping the old top-level fields as an implicit
  single-user fallback — one config shape to document and validate, not two.
- **A session per user, without a `session_ref` per user** — see the
  reversal above: `root_dir` plus the user's `name` derive it, rather than
  each entry writing one by hand.

## What wasn't on the original list, and is the actually hard part

- **The storage schema had no tenant scoping.** `conversations`, `messages`,
  and `conversation_participants` all assumed one phone, one owner. Fixed —
  see [What's already done](#whats-already-done-storage-schema).
- **The concurrency model changes from "one loop" to "N loops."**
  `Bridge.Run` is a single goroutine per gm/mm pair today (see
  [WALKTHROUGH.md §7](WALKTHROUGH.md)); a multi-tenant daemon runs one such
  loop per paired user, concurrently, in the same process. See
  [Concurrency and resource cost](#concurrency-and-resource-cost-of-n-tenants)
  for why this is less alarming than it sounds.
- **Cross-tenant data leakage is a real risk, not just a modeling nicety.**
  Google's `gmessages_conversation_id` / `gmessages_message_id` are opaque IDs
  scoped to *one* Google account; nothing guarantees they don't collide across
  two different people's accounts. The phone-number lookup
  (`FindConversationByPhone`, `internal/storage/conversations.go`) is the
  sharpest edge here: without tenant scoping, two users who both have the same
  contact's number could, in principle, have their conversations found or
  merged by that shared phone number, which is a privacy bug, not just a
  data-modeling one.

## What's already done: storage schema

The schema now carries a `tenant` column (`internal/storage/sqlite.go` and
`postgres.go`) on `conversations`, `conversation_participants`, `messages`,
and `outbound_pending`, defaulting to `''`. It's part of the **primary keys**
on `conversations` (`tenant, gmessages_conversation_id`) and `messages`
(`tenant, gmessages_message_id`), and of the foreign key from
`conversation_participants` back to `conversations` — not just an extra
column sitting next to unscoped keys. `participants_by_phone` now leads with
`tenant` too, since `FindConversationByPhone` is the sharpest cross-tenant
leak risk (see above). `outbound_pending` carries the column for consistency
but keeps `tmp_id` alone as its key, since that's a `uuid.NewString()` value
and already globally unique regardless of tenant.

Getting this right initially — rather than adding it later via `ALTER TABLE`
— matters because SQLite cannot alter a primary key in place; changing one
after the fact means a table rebuild (new table, copy rows, drop, rename),
which is real schema surgery with its own failure modes. That cost was worth
avoiding, and was avoidable *only* because this codebase has never shipped: no
installation anywhere has data in the old shape, so there was nothing to
migrate *from* — the "migration" is just what a fresh database has always
looked like. This is a one-time window; once a version ships with the old
shape, changing the keys later would require the real rebuild-migration this
avoided.

The two `ON CONFLICT` clauses that target these keys
(`conversations.go`'s `SaveConversation`, `messages.go`'s `SaveMessage`) were
updated to `ON CONFLICT(tenant, ...)` to match — SQL requires the conflict
target to name the actual unique constraint. No other query changed: every
`WHERE` clause, `SELECT`, and Go function signature is untouched, because
every row today still implicitly gets `tenant = ''` from the column default,
and nothing yet asks for a different one. Verified by the full storage test
suite, including the reopen-after-migration test.

**Done, not schema-related:** every method in `internal/storage`
(`GetConversation`, `FindConversationByPhone`, `SaveMessage`, ...) now takes a
`tenant string` and filters by it, with every caller in `internal/bridge`
passing `Bridge.tenant` through. No further migration was needed, as
predicted — this was purely a `internal/storage`/`internal/bridge` change.
Tenant isolation itself (two tenants with a contact sharing a phone number, or
sharing a destination channel) is covered by
`internal/storage.TestTenantIsolation`.

## Concurrency and resource cost of N tenants

Roughly right, with the details filled in. Per active tenant, steady-state
concurrency is about four goroutines, not one (see
[WALKTHROUGH.md §7](WALKTHROUGH.md)):

1. libgm's own goroutine, maintaining that tenant's connection to Google.
2. Two pump goroutines draining each side's event queue into a Go channel.
3. `Bridge.Run` itself, one `for { select {...} }` loop.

All four spend almost all their time blocked in a channel receive or a
network read — genuinely idle, not spinning. Ten tenants is on the order of
40 goroutines. Go goroutines start with a ~2KB stack and the runtime
scheduler is built to handle tens of thousands of them; 40 mostly-blocked
goroutines is not a resource concern at any scale this project would plausibly
reach.

The real per-tenant cost isn't the goroutines, it's the **persistent
connections**: one long-lived connection to Google per paired session (with
its own keepalive ping on `gmessages.ping_interval_seconds`, default 60s, and
an hourly auth-token refresh) and, if tenants share one Mattermost bot
account, one shared WebSocket rather than N — which is actually a point in
favor of the shared-bot-account design above. Ten open sockets and ten
lightweight timers is still nothing to worry about; it would only start to
matter at a scale (hundreds of tenants on one process) this design was never
aimed at.

## Option A in more detail: one instance per person

Kept on the table, not discarded — nothing below option B is committed, and
this needs zero code to work today.

- **What it looks like operationally.** One container/process per person,
  each with its own `config.json`, `root_dir`, and `state_dir`/database, all
  pointed at the same (or different) Mattermost server. Whether each person gets their own bot account or they share one is
  an independent choice, orthogonal to which shape (A or B) is picked.
- **Why it stays attractive.** Complete isolation is free: no tenant column,
  no cross-tenant leak risk (see
  [What wasn't on the original list](#what-wasnt-on-the-original-list-and-is-the-actually-hard-part)
  above — that whole problem class doesn't exist here, because there is only
  ever one tenant per process, per database). No concurrency model change,
  no config-shape change, no pairing-CLI change. If msggw ever needs to run
  for people who don't trust each other with even a shared process — let
  alone a shared database row — this is the only shape that gives that
  guarantee by construction rather than by careful scoping.
- **What it costs instead.** The cost moves from code to operations: N
  containers to run and upgrade, N configs to hand out and keep in sync with
  new release settings, N sets of credentials/secrets to manage. This scales
  linearly and by hand unless something else (systemd templates, a compose
  file, an orchestrator) absorbs the repetition — msggw itself would do
  nothing to help here, since by definition it doesn't know about the other
  instances.
- **When to reach for it instead of B.** Small trusted-operator counts where
  the operational repetition is still cheap by hand; a hosting or compliance
  reason to demand hard isolation regardless of engineering effort; or simply
  wanting to ship *something* for multiple people sooner, since A requires no
  msggw changes at all, while B's remaining work (tenant threading through
  storage, pairing, config) is still unscheduled.

## Where this stands

Option B is built, in the order originally planned: `tenant string` threaded
through `internal/storage` and its callers in `internal/bridge` → a tenant
argument on `msg-gw pair`/`logout`/`status` → config's `gmessages`/`routing`
moved from top-level fields into a `users: []` array (a clean break — no
legacy single-user shape kept alongside it) → group-chat channel
auto-provisioning included, gated by `routing.join_channels`.

One deliberate behavior change that fell out of this: previously, a daemon
that could not pair gave up and the whole process exited non-zero. With
several tenants, one person's broken or not-yet-paired session must not take
down everyone else's working bridge — so a per-tenant startup failure is now
logged and that tenant's goroutine simply does not start; the daemon itself,
and every other tenant, keeps running.

## Client-mode pairing — a second piece of groundwork

Separate from the above, but related: cookie-based pairing (see
`docs/CONFIGURATION.md#pairing`) needs the operator's own device to sign into
Google, not the daemon's host — signing in from a VPS's IP, with no prior
login history for that account, is exactly the profile Google's fraud
detection flags. "Client mode" moves that step to wherever the operator
actually is: `msg-gw pair --remote` itself is the client — it signs into
Google on the operator's own device (by default via `internal/browserauth`,
which opens a local browser and captures the resulting cookies automatically;
see `docs/RUNNING.md`'s "Fallback: manual cookies" for the headless
alternative), then hands the resulting session material to the daemon over
the network instead of a human copying a `cookies.json` by hand.

Planned build order — all three steps are now done:

1. **The HTTP(S) listener** (`internal/listener`, config's `listener` block).
   Built before there was anything real to route to: it serves `/healthz`,
   plus whatever else is mounted onto it. TLS falls back to plain HTTP,
   loudly, if `cert_file`/`key_file` are missing or unusable — see
   `docs/CONFIGURATION.md#listener`.
2. **Multi-tenancy** (the rest of this document). The pairing endpoint below
   routes by tenant: each `users[]` entry's `name` identifies which tenant's
   derived session a client registers against.
3. **The client side** (`internal/pairproto`, `internal/pairclient`,
   `internal/browserauth`, `cmd/pairserver.go`). `msg-gw pair NAME --remote
   https://daemon:PORT --token TOKEN` runs entirely on the operator's own
   device: it obtains cookies the same way local pairing does — by default,
   `internal/browserauth` drives a local Chrome/Chromium/Edge to capture them
   automatically; `--cookies-file`, piped stdin, or `--no-browser` remain as
   the fallback for headless devices — then POSTs them to the daemon's
   `/pair/{name}/start` and `/pair/{name}/wait` instead of calling
   `internal/gmessages` locally. *How* cookies are obtained changed with
   `internal/browserauth` — it's automated now, not a manual devtools copy —
   but *where the daemon-facing half of pairing runs* didn't: still the
   operator's own device, never the daemon's host, which is the part that
   actually matters. A VPS's IP signing in with no prior login history for
   the account is what Google's fraud detection flags, and that step never
   touches the daemon's host in this shape.

   Each tenant opts in per user, via `users[].remote_pairing.token_ref` (a
   [secret reference](CONFIGURATION.md#secret-references), same mechanism as
   `mattermost.token_ref`) — a bearer token the client must present. Empty or
   unset disables remote pairing for that user: the endpoint answers 403
   rather than accepting an unauthenticated cookie handoff. `/start` returns
   the emoji to show; `/wait` blocks until the phone confirms, then the
   daemon runs the same `Verify` reconnect-and-fetch-conversations step the
   local flow does, and only then responds — so a client that got a 200 from
   `/wait` has exactly the same guarantee local pairing gives. A pairing that
   is started but never waited on (a crashed client) is closed and forgotten
   after five minutes, so it cannot leak an open connection to Google
   indefinitely.
