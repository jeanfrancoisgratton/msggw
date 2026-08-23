# Multi-tenancy — a parked design conversation

**Status: the direction is decided — single daemon, multiple tenants (option
B below) — the query-layer and pairing/config/routing work is not scheduled.**
This document records the conversation that led there. One piece of
groundwork is already done: the storage schema (see
[What's already done](#whats-already-done-storage-schema)) is now
tenant-shaped, not just tenant-flagged — everything else below is still a
plan.

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

- **Pairing aware of a tenant** — right. `gmessages.session_ref` already
  supports being a template; `msg-gw pair $USER` mostly needs a CLI argument
  threaded through to pick which reference to read/write. Cheap.
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
  message. A *new* Mattermost channel auto-provisioned per user for their
  group texts would be genuinely new work — today `routing.join_channels`
  only joins an **existing** channel; there is no channel-creation call
  anywhere in `internal/mattermost`.
- **Config shape** — global settings (`mattermost`, `log`, `backend`) stay as
  they are; a `users: []` array would hold, per entry, roughly what
  `gmessages` + `routing` hold today. This is a natural generalization of the
  existing config shape, not a rewrite of it.
- **`session_ref` per user** — straightforward, same mechanism as pairing
  above.

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

**What's still ahead, not schema-related:** nothing in the Go query layer
accepts or filters by a tenant value yet — every method in
`internal/storage` (`GetConversation`, `FindConversationByPhone`,
`SaveMessage`, ...) needs a `tenant string` parameter threaded through before
two tenants could safely share one database. The schema will not force
another migration when that happens; it's purely `internal/storage` and its
callers in `internal/bridge`.

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
  each with its own `config.json`, `gmessages.session_ref`, and
  `state_dir`/database, all pointed at the same (or different) Mattermost
  server. Whether each person gets their own bot account or they share one is
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

Direction chosen (option B); nothing beyond the storage schema is scheduled.
The natural order of the remaining work: thread a `tenant string` parameter
through `internal/storage`'s methods and their callers in `internal/bridge`
→ thread a tenant identifier through pairing (`msg-gw pair $USER`) → extend
config to a `users: []` shape → decide whether group-chat channel
auto-provisioning is in scope for a first version or deferred.
