# Configuration

`message-gateway` reads a single JSON file. It looks for it in this order:

1. the path given to `--config` / `-c`;
2. `/etc/msggw/config.json`;
3. `$XDG_CONFIG_HOME/msggw/config.json` (usually `~/.config/msggw/config.json`).

Print a starting point and check it:

```bash
message-gateway config sample > /etc/msggw/config.json
$EDITOR /etc/msggw/config.json
message-gateway config check
```

`config check` validates the file **and** resolves every secret it refers to,
without contacting Google or Mattermost. It never prints a secret's value —
only where it came from and how many bytes were read.

Unknown keys are rejected rather than ignored, so a typo is an error rather
than a silently-applied default.

---

## Table of contents

- [Secret references](#secret-references)
- [Top level](#top-level)
  - [Storage backend](#storage-backend)
- [`log`](#log)
- [`vault`](#vault)
- [`mattermost`](#mattermost)
- [`listener`](#listener)
- [`users`](#users)
  - [`gmessages`](#gmessages)
  - [Pairing](#pairing)
  - [Client-mode pairing](#client-mode-pairing)
  - [`routing`](#routing)
    - [Destinations](#destinations)
    - [Rules](#rules)
    - [Threads](#threads)
    - [Delivery status](#delivery-status)
- [Worked examples](#worked-examples)

---

## Secret references

No credential is written into the configuration file as a value. Every one of
them is a **reference** of the form `<scheme>:<location>`:

| Reference | Meaning | Writable |
|---|---|:--:|
| `env:MM_BOT_TOKEN` | environment variable | no |
| `file:/etc/msggw/mm.token` | plain file, must be mode `0600` or tighter | yes |
| `encoded:/var/lib/msggw/session.enc` | file encoded with `helperFunctions` (empty passphrase) | yes |
| `encoded:/path/to/file#PASSPHRASE_VAR` | same, with the passphrase read from `$PASSPHRASE_VAR` | yes |
| `vault:secrets/msggw#bot_token` | HashiCorp Vault KV, via `vaultLib` | yes |
| `literal:xoxb-...` | inline in the config file | no |

A reference with no recognised scheme is an error. That is deliberate: a bare
string would otherwise be indistinguishable from a token accidentally pasted
into a world-readable file.

**Four settings are always a reference**, because they hold a credential:
`users[].gmessages.session_ref`, `mattermost.token_ref`, `backend.postgres.dsn_ref`
and `vault.token_ref`. Elsewhere, a reference is optional: `backend.sqlite.path`,
`mattermost.url`, `vault.address`, and every routing [destination](#destinations)
field (`team`, `channel`, `channel_id`, `user`, `users`) normally take a plain
value, but also accept a reference for an operator who wants the value —
sensitive or not — kept out of the config file too. A plain value works
exactly as before: it is only treated as a reference when its prefix, up to
the first `:`, matches one of the schemes above (so a `https://...` URL, an
ordinary filesystem path, or a bare username all pass through untouched).

**Writability matters for one setting only.** `users[].gmessages.session_ref` has
to be writable, because `libgm` refreshes its Google auth token roughly hourly and
the refreshed session must be persisted; a session behind `env:` or `literal:` would
force a re-pairing on every restart. The configuration rejects those two schemes
for that field.

`file:` and `encoded:` writes are atomic — written to a temporary file in the
same directory, `chmod 0600`, then renamed — so a crash mid-write cannot leave a
truncated session behind.

> **On `encoded:`** — `helperFunctions`' encoding is AES-256-CFB, which is
> unauthenticated. A wrong passphrase produces plausible-looking garbage rather
> than an error. For the session this is caught downstream, where the result is
> parsed as JSON; for an arbitrary token it is not caught at all. Use `vault:`
> when the difference matters.

---

## Top level

| Key | Type | Default | Meaning |
|---|---|---|---|
| `state_dir` | string | `/var/lib/msggw` | Everything persisted that is not a secret. |
| `backend` | object | — | Storage backend selection and settings; see [Storage backend](#storage-backend). |
| `users` | list of objects | — | **Required, at least one entry.** One tenant per entry — see [`users`](#users). |

### Storage backend

`message-gateway` keeps its bridge state — which Mattermost thread stands for which
Google Messages conversation, and which post stands for which message — in a
SQL database. Two backends are supported, chosen and configured through the
`backend` object:

| Key | Type | Default | Meaning |
|---|---|---|---|
| `backend.driver` | `sqlite` \| `postgres` | `sqlite` | Which storage backend to use. |
| `backend.sqlite.path` | string or [secret reference](#secret-references) | `msggw.db` | SQLite file, read only when `backend.driver` is `sqlite`. A relative path resolves inside `state_dir`. |
| `backend.postgres.dsn_ref` | secret reference | — | PostgreSQL DSN, read only when `backend.driver` is `postgres`. **Required** in that case. |

Both `backend.sqlite` and `backend.postgres` may be filled in at the same
time — only the block named by `backend.driver` is ever read. This lets the
sample configuration ship both blocks fully populated, so switching backends
later is a one-line change to `backend.driver` rather than a round trip
through this document to figure out what the other block needs.

- **`sqlite`** (the default) — a single file under `state_dir`. This is what
  the daemon has actually been run against: the expected volume is one
  person's text messages, which SQLite handles comfortably.
- **`postgres`** — for an operator who wants the state store on a separate
  server, under its own backup and HA story, rather than a file next to the
  daemon. Because the DSN is just an address, `postgres` is the natural choice
  when the daemon itself runs somewhere ephemeral (a container that gets
  recreated, an autoscaled host) and the database needs to outlive it —
  there is no local file to lose.

Switching backends does not migrate existing data: each keeps its own schema
version. Make sure the backend you switch *to* is already provisioned (the
Postgres database exists and is reachable, or the SQLite file is the one you
expect) before flipping `backend.driver` — the daemon does not carry data
across.

**Persistence with `sqlite` is entirely the operator's responsibility.** The
daemon does nothing beyond writing to `backend.sqlite.path`; there is no
built-in backup or replication. In particular, if `message-gateway` runs in a
container, `state_dir` sits in the container's writable layer by default and
is lost whenever the container is recreated — mount `state_dir` (or at least
the SQLite file's directory) onto a persistent volume from the host or
whatever the container platform provides for durable storage. That volume
must behave like a local filesystem: SQLite depends on OS-level file locking
to stay correct with a single writer, and that locking is unreliable over
network filesystems (NFS and some cluster/CSI volume backends in particular)
— using one of those as the volume can corrupt the database under concurrent
access. A local, host-backed volume is the safe choice.

A PostgreSQL DSN contains a password, so — like `mattermost.token_ref` — it is
given as a [secret reference](#secret-references), not a plain string:

```json
"backend": {
  "driver": "postgres",
  "sqlite": {
    "path": "msggw.db"
  },
  "postgres": {
    "dsn_ref": "vault:secrets/msggw#database_dsn"
  }
}
```

The resolved value is the DSN itself, e.g.
`postgres://user:pass@host:5432/msggw?sslmode=disable`. `backend.postgres.dsn_ref`
does not need to be writable — unlike a user's `gmessages.session_ref` — since
the daemon never rewrites it.

---

## `log`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `level` | `debug` \| `info` \| `warn` \| `error` | `info` | Also bounds what `libgm` logs. |
| `format` | `text` \| `json` | `text` | |

`libgm` logs through zerolog; its output is routed into the daemon's logger with
its own severity preserved, so there is one stream and one format to configure.

---

## `vault`

Only needed when some reference uses the `vault:` scheme. Every field is
optional: anything left empty falls back to the usual `VAULT_*` environment
variables.

| Key | Type | Meaning |
|---|---|---|
| `address` | string or [secret reference](#secret-references) | Falls back to `VAULT_ADDR`. May **not** itself be a `vault:` reference. |
| `token_ref` | secret reference | The Vault token. May **not** itself be a `vault:` reference. |
| `namespace` | string | Vault Enterprise namespace |
| `ca_cert_path`, `client_cert_path`, `client_key_path` | string | TLS material |
| `tls_skip_verify` | bool | Disables TLS verification |
| `timeout_seconds` | int | Vault HTTP timeout |

A `vault:` reference is written `vault:<mount>/<path>#<field>`, where `<mount>`
is the KV mount name. For a secret reachable at `secrets/data/msggw`, the
mount is `secrets` and the path is `msggw`.

---

## `mattermost`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `url` | string or [secret reference](#secret-references) | — | **Required.** Server root, e.g. `https://mattermost.example.net`. |
| `token_ref` | secret reference | — | **Required.** The bot account's personal access token. |
| `insecure_skip_verify` | bool | `false` | Only for a self-signed certificate outside the host trust store. |
| `reconnect_backoff_seconds` | int | `5` | Initial WebSocket retry delay; doubles up to one minute. |
| `request_timeout_seconds` | int | `30` | Bound on a single REST call. |

The daemon uses a **bot account**, not an incoming webhook. A webhook can only
push messages one way; the bot account is what makes replies, file uploads,
post edits and reactions possible. See [SOLUTION.md](SOLUTION.md).

Creating the account, in Mattermost: **System Console → Integrations → Bot
Accounts → Add Bot Account**, then create an access token for it. If a user's
`routing.join_channels` is off, add the bot to every channel that user routes
to.

There is exactly one bot account and one Mattermost connection, shared by
every user in [`users`](#users) — not one per tenant.

---

## `listener`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `port` | int | `0` | Port the HTTP(S) listener binds to, on all interfaces. `0` disables the listener entirely — there is no separate enable flag. |
| `cert_file` | string | — | TLS certificate, as a plain filesystem path (not a [secret reference](#secret-references)). |
| `key_file` | string | — | TLS private key, as a plain filesystem path. |

This listener serves client-mode pairing (a client running on the operator's
own device registers pairing material with the daemon, so the device doing
the actual Google sign-in is never the daemon's own host — see
[Client-mode pairing](#client-mode-pairing) and `docs/MULTI-TENANCY.md`) at
`/pair/{name}/start` and `/pair/{name}/wait`, plus a `/healthz` check. Both
`/pair` routes require a per-user bearer token
(`users[].remote_pairing.token_ref`); a user with no token configured gets a
403 from them.

**TLS falls back to plain HTTP, loudly, rather than refusing to start.** If
`cert_file`/`key_file` are unset, unreadable, or otherwise fail to load as a
key pair, the listener still starts — on plain HTTP — but logs a `warn` line
saying exactly why, every time. This is deliberate: a config problem here
should not take the whole daemon down, but silently serving something that
will eventually carry pairing cookies over an unencrypted port is the kind of
mistake that must never happen quietly. Check the daemon's logs after
changing anything under `listener` to confirm TLS is actually in effect.

---

## `users`

`users` is a list, one entry per tenant — one paired phone, with its own
session and its own routing. There is always at least one; a single-person
deployment is just a `users` array of length one, not a separate shape.
Everything else in this file (`mattermost`, `backend`, `log`, `vault`,
`listener`) is shared across every entry.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `name` | string | — | **Required, and unique among `users`.** Identifies this tenant in `msg-gw pair NAME`, in `status` output, and as the tenant column's value in storage. |
| `gmessages` | object | — | This user's Google Messages session and behaviour; see [`gmessages`](#gmessages). |
| `routing` | object | — | This user's routing; see [`routing`](#routing). |
| `remote_pairing` | object | — | Enables this user for client-mode pairing over the `listener`; see [Client-mode pairing](#client-mode-pairing). |

```json
"users": [
  { "name": "jfgratton", "gmessages": { "session_ref": "..." }, "routing": { "..." } },
  { "name": "kiddo",     "gmessages": { "session_ref": "..." }, "routing": { "..." } }
]
```

Each user runs in its own goroutine once the daemon starts, entirely
independently of the others: one person's session being unpaired, revoked, or
otherwise broken is logged and that user's bridge simply does not start — it
does not stop any other user's bridge, or the daemon as a whole.

### `gmessages`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `session_ref` | secret reference | — | **Required.** Where this user's paired-device session lives. Must be writable. |
| `ping_interval_seconds` | int | `60` | How often to ping the phone. `libgm` ignores anything outside 60–14400. |
| `force_rcs` | bool | `false` | Ask the phone to send over RCS rather than latching to SMS. Only applied to conversations that are already RCS and not latched. |
| `mark_read_on_bridge` | bool | `false` | Mark a conversation read on the phone once its message reaches Mattermost. Off by default because it also silences the phone's own notifications. |
| `backfill_count` | int | `0` | How many recent messages to post when a conversation is first bridged. `0` disables backfill. |

### Pairing

Pairing itself is not a config setting — it is a separate, one-time step, once
per user:

```bash
message-gateway pair NAME --cookies-file cookies.json
```

`NAME` must match one of the `name` entries under `users`.

Google retired QR-code device pairing, so this authenticates as your Google
account instead. Sign into `https://messages.google.com/web` in a **private**
browser window, open devtools, and copy the `SID`, `HSID`, `SSID`, `OSID`,
`APISID` and `SAPISID` cookies (and `__Secure-1PSIDTS` if present) into a JSON
file:

```json
{"SID": "...", "HSID": "...", "SSID": "...", "OSID": "...",
 "APISID": "...", "SAPISID": "...", "__Secure-1PSIDTS": "..."}
```

Pass that file with `--cookies-file`, or pipe the JSON to stdin instead. The
daemon then shows an emoji; tapping the matching one on Google Messages on the
phone confirms the pairing. On success, the session is written to that user's
`gmessages.session_ref`, and `message-gateway daemon` needs no further
interactive step to use it.

**Checking whether a user has already paired** — a session at their
`gmessages.session_ref` *is* the pairing state; there is no separate flag.
`message-gateway status [NAME]` reads that reference (for every user, or just
`NAME`) and reports `NOT PAIRED — run "msg-gw pair NAME"` or `paired with phone
<id>` (`--offline` skips the network round-trip that confirms Google still
honours the session). Equivalently, checking for a non-empty file (or secret)
at that reference works directly — an empty file counts as "not paired," the
same as a missing one (this is what `message-gateway logout NAME --local-only`
leaves behind).

**Restarting does not re-trigger pairing.** `daemon` loads each user's stored
session on startup and reconnects with it; it never launches the pairing flow
itself. As long as `gmessages.session_ref` sits on storage that survives a
restart — the same volume-mount caveat as [`backend.sqlite.path`](#storage-backend)
— a restarted container reconnects every user silently.

**Pairing a user in an unattended container.** If a user has no session yet,
that user's bridge does not fail the whole daemon: it retries for 5 attempts,
60 seconds apart (logging a `warn` line each time), before giving up on that
user alone — a four-minute window to pair from a separate step:

```bash
docker exec -it msggw message-gateway pair NAME --cookies-file cookies.json
docker logs -f msggw
```

Treat the cookies file itself as a bearer credential for the Google account —
delete it once pairing succeeds, and keep it out of anywhere `docker logs`
or shell history would retain it.

### Client-mode pairing

Everything above runs the pairing flow *on the daemon's own host* — cookies
are read there, and libgm signs into Google from there. That means the
daemon's host is what Google sees logging into the account, which for a VPS
with no prior login history for that account is exactly the profile Google's
fraud detection flags.

Client-mode pairing moves that step onto the operator's own device instead.
It needs two things configured:

1. **The listener is enabled** — `listener.port` is non-zero (see
   [`listener`](#listener)), ideally with TLS, since pairing cookies travel
   over this connection.
2. **The user opts in** — `users[].remote_pairing.token_ref` is set to a
   [secret reference](#secret-references) resolving to a bearer token, the
   same mechanism `mattermost.token_ref` uses:

   ```json
   "remote_pairing": {
     "token_ref": "vault:secrets/msggw#jfgratton_pairing_token"
   }
   ```

   Generate the token yourself (anything unguessable — `openssl rand -hex 32`
   is fine) and hand it to that user out of band. Leaving `token_ref` unset,
   or empty, disables remote pairing for that user: the endpoint answers 403
   rather than accepting an unauthenticated cookie handoff.

With both in place, the operator runs the exact same `msg-gw pair` command
they would locally, but on their own laptop or phone, with `--remote` and
`--token` added:

```bash
msg-gw pair myuser1@gmail.com \
  --remote https://msggw.example.net:8443 \
  --token-file ~/.msggw-pairing-token \
  --cookies-file cookies.json
```

`--remote` needs no other `msg-gw` configuration on that machine — no `-c`
config file, no Vault access, nothing beyond the daemon's URL and this one
user's token. The token itself can come from `--token`, `--token-file`, or
the `MSGGW_PAIR_TOKEN` environment variable. `--insecure-skip-verify` skips
TLS certificate verification, for testing against a self-signed listener
only.

Cookies are still read the same way as local pairing — `--cookies-file` or
stdin, still the `SID`/`HSID`/`SSID`/`OSID`/`APISID`/`SAPISID` set copied out
of a signed-in browser session at `https://messages.google.com/web`. What
changes is that the daemon never has to be told them by hand and never signs
into Google itself: the daemon relays the emoji back, waits for the phone to
confirm, verifies the session by reconnecting, and only then reports success —
the same guarantee local pairing gives, over the network instead of a shell
on the daemon's host.

### `routing`

This is where you decide **where one user's messages end up in Mattermost**.
Nothing about the layout is hard-coded, and each user has their own —
routing is not shared.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `default` | destination | — | **Required.** Used for every conversation no rule matches. |
| `rules` | list of rules | `[]` | Evaluated in order; the first match wins. |
| `thread_per_conversation` | bool | `true` | See [Threads](#threads). |
| `post_delivery_status` | bool | `false` | See [Delivery status](#delivery-status). |
| `join_channels` | bool | `false` | Let the daemon add the bot to a channel it is not a member of, **or create a named channel that does not exist yet at all** (as a private channel). Without it, an unjoined or missing channel is an error. |

#### Destinations

```json
{ "type": "channel",    "team": "myteam", "channel": "messages" }
{ "type": "channel_id", "channel_id": "kzq1p3wj4bg9jfk8x9ffxxxxxx" }
{ "type": "direct",     "user": "jfgratton" }
{ "type": "group",      "users": ["jfgratton", "someone-else"] }
```

- `channel` — a named channel of a named team, by URL name. With
  `join_channels` on, the channel is created (private) if it does not exist.
- `channel_id` — the raw 26-character channel ID. Use this when the channel's
  URL name may change. Never auto-created: naming an ID means it must already
  exist.
- `direct` — the direct-message channel between the bot and one user. This is
  the "everything shows up in my DMs with the bot" layout.
- `group` — a group direct message between the bot and 2–7 users.

`team`, `channel`, `channel_id`, `user` and each entry of `users` accept a
[secret reference](#secret-references) in place of a plain value.

#### Rules

```json
{
  "name": "family goes to my DMs",
  "phones": ["+1 514 555-1212", "+15145551213"],
  "destination": { "type": "direct", "user": "jfgratton" }
}
```

| Key | Meaning |
|---|---|
| `name` | Label for logs. No effect on matching. |
| `conversation_ids` | Exact Google Messages conversation IDs. |
| `phones` | Any participant's number. Spaces, dashes, dots and parentheses are ignored on both sides, so `+1 (514) 555-1212` matches `+15145551212`. Your own number is never matched. |
| `name_pattern` | Go regular expression against the conversation's display name — the contact name, the group title, or the participants' numbers when there is no name. |
| `groups_only` / `directs_only` | Restrict the rule to group or one-to-one conversations. Setting both is rejected. |
| `destination` | Where matching conversations go. |

Matching semantics:

- The **shape** filters (`groups_only`, `directs_only`) are *conditions*: they
  must hold.
- The **identity** criteria (`conversation_ids`, `phones`, `name_pattern`) are
  *alternatives*: any one of them matching is enough.
- A rule with only a shape filter matches every conversation of that shape.
- A rule with no criteria at all is rejected, so a half-written rule cannot
  quietly capture every conversation.

A rule is applied when a conversation is **first** bridged. Changing the rules
later does not move existing threads — that would strand their history — so it
only affects conversations bridged after the change.

#### Threads

With `thread_per_conversation: true` (the default), each Google Messages
conversation gets one Mattermost root post, and every message in it becomes a
reply in that thread. This is the layout in [SOLUTION.md](SOLUTION.md):

```text
#messages

┌─ +1 514 555-1212 ───────────────────
│  16:42  Contact — I'm leaving now
│  16:43  jfgratton — OK. Text me when you arrive.
└──────────────────────────────────────
```

Replying in the thread sends the reply back over RCS.

With `thread_per_conversation: false`, every message is a top-level post. That
only works when a channel is dedicated to a single conversation: without a
thread to identify it, the daemon has to infer the conversation from the channel
alone, and it refuses to guess when a channel holds more than one.

#### Delivery status

With `post_delivery_status: true`, an outgoing message's state shows up as a
reaction on its post:

| Reaction | Meaning |
|---|---|
| ⏳ `:hourglass_flowing_sand:` | still being sent |
| ✉️ `:envelope:` | left the phone |
| ✅ `:white_check_mark:` | delivered |
| 👀 `:eyes:` | read — RCS only; SMS never reports this |
| ⚠️ `:warning:` | failed |

Only outgoing messages are decorated. A message that cannot be sent at all also
gets a reply in the thread saying why, so a failure is never invisible to the
person who typed it.

---

## Worked examples

Each snippet below is one user's `routing` block — i.e. what goes inside one
entry of the top-level `users` array, alongside that user's `gmessages` block.

### Everything in one channel

```json
"routing": {
  "default": { "type": "channel", "team": "myteam", "channel": "messages" },
  "join_channels": true
}
```

### Everything in your DMs with the bot

```json
"routing": {
  "default": { "type": "direct", "user": "jfgratton" }
}
```

### Family in DMs, group chats in their own channel, everything else in #messages

```json
"routing": {
  "default": { "type": "channel", "team": "myteam", "channel": "messages" },
  "rules": [
    {
      "name": "family",
      "phones": ["+1 514 555-1212", "+1 514 555-1213"],
      "destination": { "type": "direct", "user": "jfgratton" }
    },
    {
      "name": "group chats",
      "groups_only": true,
      "destination": { "type": "channel", "team": "myteam", "channel": "group-messages" }
    }
  ],
  "join_channels": true
}
```

### One conversation pinned to its own channel, with no threading

```json
"routing": {
  "default": { "type": "channel", "team": "myteam", "channel": "messages" },
  "rules": [
    {
      "name": "the on-call number",
      "phones": ["+1 514 555-9999"],
      "destination": { "type": "channel", "team": "myteam", "channel": "on-call-sms" }
    }
  ],
  "thread_per_conversation": false
}
```

Note that `thread_per_conversation` is per user, not per rule. Turning it off
puts *every* one of that user's conversations in top-level posts, so use it
only when each routed channel holds exactly one conversation.

### Two people, sharing one daemon and one bot

```json
"users": [
  {
    "name": "jfgratton",
    "gmessages": { "session_ref": "encoded:/data/gmessages/jfgratton.session.enc" },
    "routing": { "default": { "type": "direct", "user": "jfgratton" } }
  },
  {
    "name": "kiddo",
    "gmessages": { "session_ref": "encoded:/data/gmessages/kiddo.session.enc" },
    "routing": { "default": { "type": "direct", "user": "kiddo" } }
  }
]
```

Each user pairs separately (`msg-gw pair jfgratton`, `msg-gw pair kiddo`) and
runs independently once the daemon starts — one's phone being unreachable, or
never having paired at all, has no effect on the other's.
