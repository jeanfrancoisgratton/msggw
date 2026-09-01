# Running msggw

There are two different hats worn around a `message-gateway` deployment, and
this document has one section for each:

- **The operator**, who builds and runs the daemon, owns the configuration
  file, and creates the Mattermost bot account. See
  [Setting up the daemon](#setting-up-the-daemon).
- **The user (client)**, who pairs their own Android phone's Google Messages
  app against one `users[]` entry the operator has prepared for them. See
  [Setting up a client (user)](#setting-up-a-client-user).

A single-person deployment is both hats worn by the same person, one after
the other: set up the daemon, then pair yourself as its one user.

---

## Table of contents

- [Setting up the daemon](#setting-up-the-daemon)
  - [1. Build](#1-build)
  - [2. Create the Mattermost bot account](#2-create-the-mattermost-bot-account)
  - [3. Write the configuration](#3-write-the-configuration)
  - [4. Add one `users[]` entry per person](#4-add-one-users-entry-per-person)
  - [5. Add, edit and remove routing rules](#5-add-edit-and-remove-routing-rules)
  - [6. Validate and run](#6-validate-and-run)
  - [7. Keep it running](#7-keep-it-running)
  - [Reloading a running daemon](#reloading-a-running-daemon)
- [Setting up a client (user)](#setting-up-a-client-user)
  - [Local pairing — you have shell access to the daemon's host](#local-pairing--you-have-shell-access-to-the-daemons-host)
  - [Remote pairing — client mode](#remote-pairing--client-mode)
  - [Checking your own status](#checking-your-own-status)
  - [Unpairing](#unpairing)
  - [Fallback: manual cookies (headless / no-browser environments)](#fallback-manual-cookies-headless--no-browser-environments)

---

## Setting up the daemon

This is the operator's side: one running process, one configuration file,
one Mattermost bot account, shared by every paired user.

### 1. Build

Go 1.27 or newer. The build is fully static (`CGO_ENABLED=0` — no cgo, no
libc to match at deploy time):

```bash
cd src
./build.sh /opt/sbin      # builds /opt/sbin/message-gateway
go test ./...             # optional, but cheap insurance
```

Packaging stubs for Debian, RPM-based distros, Arch and Alpine live under
`__debian/`, `__redhat/`, `__archlinux/` and `__alpine/` at the repository
root, each with its own `Makefile`, if you'd rather build a distro package
than run `build.sh` by hand. There is no systemd unit shipped yet —
[Keep it running](#7-keep-it-running) below has a minimal one to adapt.

### 2. Create the Mattermost bot account

The bridge posts and replies as a **bot account** over Mattermost's REST and
WebSocket APIs — not an incoming webhook, since a webhook can't read replies,
upload files, or edit posts (see [SOLUTION.md](SOLUTION.md)).

In Mattermost: **System Console → Integrations → Bot Accounts → Add Bot
Account**, then create a personal access token for it. Keep that token
somewhere `message-gateway` can read it as a [secret
reference](CONFIGURATION.md#secret-references) — a plain file, an environment
variable, or Vault.

If a user's `routing.join_channels` is left off, also add the bot to every
channel that user's routing sends messages to; with `join_channels` on, the
bot joins (or creates, for a channel that doesn't exist) those channels
itself.

### 3. Write the configuration

`message-gateway` reads one JSON file, looked up at `--config`/`-c`, then
`/etc/msggw/config.json`, then `$XDG_CONFIG_HOME/msggw/config.json`:

```bash
message-gateway config sample > /etc/msggw/config.json
$EDITOR /etc/msggw/config.json
```

At minimum, fill in:

- `mattermost.url` and `mattermost.token_ref` — the server and the bot token
  from step 2.
- `backend` — `sqlite` (the default, a file under `state_dir`) or `postgres`;
  see [Storage backend](CONFIGURATION.md#storage-backend).
- `state_dir` — where the database and in-flight media live.
- `root_dir` — where every user's Google Messages session lives, derived
  automatically per user (see step 4 below). Both `state_dir` and `root_dir`
  should be on a **local, host-backed** volume if you're in a container, not
  a network filesystem (SQLite's locking is unreliable over NFS and
  similar).

Every credential in this file is a *reference*, never a value — see [Secret
references](CONFIGURATION.md#secret-references). The full key-by-key
reference is [docs/CONFIGURATION.md](CONFIGURATION.md).

### 4. Add one `users[]` entry per person

`users` is a list, one entry per tenant — one paired phone, its own session,
its own routing. A single-person deployment is just a `users` array of
length one:

```json
"root_dir": "/var/lib/msggw",
"users": [
  {
    "name": "jfgratton",
    "routing": {
      "default_direct": { "type": "channel", "team": "myteam", "channel": "messages" }
    }
  }
]
```

`name` is what that person will pass to `message-gateway pair NAME` — see
[Setting up a client (user)](#setting-up-a-client-user). It also feeds the
session path the daemon derives automatically:
`encoded:root_dir/gmessages/name_session.enc` — there is no `session_ref` to
write by hand, and no way for two users to collide on the same file. Routing
decides which Mattermost channel, DM, or group each conversation lands in —
see [`routing`](CONFIGURATION.md#routing) for the full set of destinations
and rules.

If any of your users will pair remotely instead of on this host (see
[Remote pairing](#remote-pairing--client-mode) below), this is also where you
enable the [`listener`](CONFIGURATION.md#listener) and set that user's
`remote_pairing.token_ref`.

Hand-writing the entry above is not the only way to get there: `message-gateway
pair NAME --mattermost-user USERNAME` creates it for you the first time NAME
is paired, routed to a direct message with USERNAME — see [Local
pairing](#local-pairing--you-have-shell-access-to-the-daemons-host) below.
Useful either way.

### 5. Add, edit and remove routing rules

A user's `routing.default_direct`/`default_group` (step 4) catch everything;
`routing.rules` let you send specific conversations somewhere else — a
contact or group routed to its own channel, ahead of the defaults. Rules are
evaluated in order, first match wins. `msg-gw rules` manages a user's rules
without hand-editing `config.json`:

```bash
message-gateway rules list jfgratton
message-gateway rules add jfgratton --name family \
  --phone "+1 514 555-1212" --phone "+1 514 555-1213" \
  --to-user jfgratton
message-gateway rules remove jfgratton 1
```

- **Create** with `rules add NAME`. It needs at least one matching criterion
  (`--conversation-id`, `--phone`, `--name-pattern`, and/or a shape filter —
  `--groups-only`/`--directs-only`) and exactly one destination
  (`--to-channel`, `--to-channel-id`, `--to-user`, `--to-users`). The new rule
  is appended to the end of that user's list.
- **Remove** with `rules remove NAME INDEX`, where `INDEX` is the 1-based
  position `rules list NAME` shows.
- **Edit**: there's no in-place edit yet — `rules remove` the old one and
  `rules add` the replacement. Since a new rule is always appended, reordering
  rules (to change which one wins first) works the same way: remove and
  re-add in the order you want.

Two things are true of every one of these commands, by construction rather
than by convention:

- They write straight to **whichever `config.json` is currently active** —
  the same file `config.Load` resolves at startup (`--config`/`-c`, then
  `/etc/msggw/config.json`, then `$XDG_CONFIG_HOME/msggw/config.json`). Run
  `rules` with the same `--config` the daemon uses (or from the same host, if
  you rely on the default paths) and there is no separate "server copy" to
  keep in sync — it's the one file.
- Every change is **sanity-checked before it touches that file**: the
  candidate configuration is validated exactly as `msg-gw config check`
  validates `config.json` at daemon startup, and is only written — atomically
  — once it loads cleanly. A rule that would leave the configuration invalid
  (bad regex, malformed destination, missing team/channel, and so on) is
  rejected with an explanation and `config.json` is left untouched.

What this does **not** do is apply the change to a running daemon — `rules
add`/`rules remove` print a reminder that you still need to run `message-gateway
reload` (or send `SIGHUP`, or restart) afterwards, in the same process, for
it to take effect. See [Reloading a running
daemon](#reloading-a-running-daemon) below. Also note that a rule only
applies to a conversation the **first** time it's bridged — changing the
rules later does not move an already-bridged conversation's existing thread,
so this is a good one to get right before a conversation shows up for the
first time, not necessarily something to reload for on every single edit.

See [`routing`](CONFIGURATION.md#routing) for the full field reference and
matching semantics, and `msg-gw rules --help` / `msg-gw rules add --help` for
the complete flag list.

### 6. Validate and run

```bash
message-gateway config check
message-gateway daemon
```

`config check` resolves every secret reference and reports where each one
came from, without ever printing a value, and without contacting Google or
Mattermost. `daemon` then connects once to Mattermost as the shared bot, and
brings up one goroutine per user in `users`, each connecting to Google
Messages with that user's stored session. A user with no session yet (not
paired) is retried for a few minutes before that one user's bridge gives up —
it does not stop the daemon or any other user's bridge. `SIGINT`/`SIGTERM`
shut it down cleanly, persisting every connected user's session first.

No user needs to be paired before the daemon starts — see [Setting up a
client (user)](#setting-up-a-client-user) for that step, which can happen
before, during, or after the daemon is running.

### 7. Keep it running

There's no shipped systemd unit yet, but a minimal one is enough to get
started:

```ini
[Unit]
Description=msggw — SMS/MMS/RCS to Mattermost gateway
After=network-online.target

[Service]
ExecStart=/opt/sbin/message-gateway daemon
ExecReload=/opt/sbin/message-gateway reload
Restart=on-failure
User=msggw

[Install]
WantedBy=multi-user.target
```

`ExecReload` wires `systemctl reload msg-gw` to [`msg-gw
reload`](#reloading-a-running-daemon) below, instead of the unit's default of
sending `SIGHUP` itself — either one works, but going through the command
gets you its confirmation message and its error if the daemon isn't running
at all.

Whatever supervises it, make sure `state_dir` (and the SQLite file inside it,
if that's your backend) and `root_dir` both survive a restart — see [Storage
backend](CONFIGURATION.md#storage-backend). Restarting the daemon does not
re-trigger pairing: it loads each user's stored session and reconnects
silently.

### Reloading a running daemon

A full restart is disruptive in a way that isn't always acceptable — most of
all in a container whose entrypoint does `exec message-gateway daemon`,
where the daemon *is* the container's PID 1: there's no supervisor sitting
above it to bring it back if it exits, and no separate process to restart it
without also restarting the container. `message-gateway reload` (or sending
the daemon `SIGHUP` directly — `kill -HUP 1` inside that container) asks the
*same, already-running process* to re-read `config.json` and pick up the
change instead:

```bash
message-gateway reload
```

This finds the daemon via a pid file it writes at
`<state_dir>/msggw.pid` on startup — resolved from the same `--config` (or
default paths) you'd give the daemon itself, so as long as `reload` is run
against the same configuration, there's nothing else to point it at. It
prints confirmation once the signal is sent, but sending a signal isn't the
same as confirming the reload worked — check the daemon's own logs for a
line starting with `reload:` to see whether the new configuration was
actually valid and got applied.

What a reload does, precisely:

- Re-reads and fully re-validates `config.json` — exactly what `msg-gw
  config check` checks. **An invalid configuration is rejected outright and
  logged**; the daemon keeps running on the configuration it already had,
  same as `msg-gw rules`/`msg-gw config` refusing to write a change that
  wouldn't load. A reload can never leave the daemon half-configured or
  down because of a typo.
- Once a new configuration is confirmed valid, restarts every user's Google
  Messages bridge, the shared Mattermost connection, and the pairing
  listener (if enabled) against it — all inside the same process, same PID,
  same container. Every currently-connected user briefly drops and
  reconnects; a user's Google Messages session itself is untouched (no
  re-pairing), same as a full restart.
- Does **not** retroactively move a conversation already bridged under an
  old rule — same caveat as [rules](#5-add-edit-and-remove-routing-rules)
  above: a rule is only consulted the first time a conversation is bridged.

A reload picks up essentially any `config.json` change — a rules change,
routing defaults, adding a `users[]` entry by hand, a new Mattermost token,
even `backend`/`state_dir`/`root_dir` themselves, since everything
downstream of the configuration is rebuilt from it on every reload, not just
at cold start. The one wrinkle: the pid file `reload` looks for always lives
under the `state_dir` the daemon *started* with — so a reload that changes
`state_dir` picks up the new one for storage and sessions, but leaves a
*later* `msg-gw reload` unable to find the running daemon at the new
`state_dir`'s pid file path. Send `SIGHUP` directly in that specific case, or
just restart normally instead.

If the new configuration is valid but the daemon can't actually connect with
it (Mattermost unreachable, the listener's port already taken, and similar)
the reload fails past the validation step and the daemon exits — the same
fate a cold start would have hit with that configuration — for your process
supervisor (or container runtime) to restart it. That's a narrower failure
mode than it sounds: content problems (a bad regex, a malformed destination)
are always caught before anything is torn down; only genuine
connectivity/environment problems can still bring the daemon down, and only
after a reload was already requested with a config to blame in the logs.

---

## Setting up a client (user)

This is the per-person, one-time step that links your Android phone's Google
Messages app to one `users[]` entry — either one the operator already created
for you (step 4 above), or one `pair` creates on the spot the first time you
run it, given `--mattermost-user`. Pairing is not just a config setting — it's
the `pair` command, run once per user, and the resulting session is what makes
`status` report "paired" from then on.

Either way, pairing needs a signed-in Google Messages web session — Google
retired QR-code device pairing, so `pair` authenticates as your Google
account instead. By default, `pair` handles that itself: it opens a browser
window for you to sign into Google, watches for the sign-in to complete, and
closes the window on its own once it has what it needs. There's nothing to
copy, no devtools, no JSON file — just the one thing only you can do, which
is proving it's your Google account.

If a browser can't be opened — a headless server, an SSH-only box, a
scripted pairing pipeline — there's still a way through. See [Fallback:
manual cookies](#fallback-manual-cookies-headless--no-browser-environments)
at the end of this section.

Which of the two pairing modes below applies depends on whether you have
shell access to the machine the daemon runs on.

### Local pairing — you have shell access to the daemon's host

This runs `pair` on the same machine as the daemon, using its configuration
file directly:

```bash
message-gateway pair NAME
```

`NAME` must match your `name` entry under `users` in the operator's
configuration — unless it doesn't exist yet, in which case add
`--mattermost-user YOUR_MATTERMOST_USERNAME` and `pair` creates it for you,
routed to a direct message with that username:

```bash
message-gateway pair NAME --mattermost-user YOUR_MATTERMOST_USERNAME
```

`--email YOUR_GOOGLE_ACCOUNT` is optional alongside that — it is recorded for
`status` to show later, never checked against the account you actually sign
into below. Neither flag does anything once NAME already exists.

Either way, a browser window opens to Google's sign-in page; sign in
there as you normally would. Once you're signed in, the window closes and
the command prints an emoji:

```text
On the phone, open Google Messages and tap this emoji when it's offered:

  🐬

Waiting for confirmation...
```

Open Google Messages on your phone and tap the matching emoji when it's
offered. Once confirmed, `pair` reconnects with the stored session and prints
the conversations it received — that's the proof the pairing actually works,
not just that it was accepted:

```text
Paired with phone <id>.
Session stored at encoded:/var/lib/msggw/jfgratton.session.enc.

Verifying the session by reconnecting...
The phone sent 12 conversations:
  ...
Pairing complete. Start the bridge with "message-gateway daemon".
```

If the daemon is already running, it picks up newly-paired sessions on its
own next reconnect attempt for that user — no restart needed if you paired
within its retry window (5 attempts, 60 seconds apart, from daemon startup).

### Remote pairing — client mode

Use this when you *don't* have shell access to the daemon's host — or
deliberately don't want to, since signing into a personal Google account from
a VPS with no prior login history for it is exactly the profile Google's
fraud detection flags. Client mode moves the Google sign-in onto your own
laptop or phone; only the resulting pairing material crosses the network to
the daemon.

This needs two things the **operator** must have already set up on their end
(step 4 above):

1. The daemon's [`listener`](CONFIGURATION.md#listener) is enabled
   (`listener.port` non-zero), ideally with TLS.
2. Your `users[].remote_pairing.token_ref` is set, and the operator has
   handed you the bearer token it resolves to, out of band.

With that token in hand, run `pair` with `--remote` — this needs no config
file, no Vault access, nothing beyond the daemon's URL and your token:

```bash
message-gateway pair NAME \
  --remote https://msggw.example.net:8443 \
  --token-file ~/.msggw-pairing-token
```

The token can also be passed with `--token`, or via the `MSGGW_PAIR_TOKEN`
environment variable, instead of `--token-file`. `--insecure-skip-verify`
skips TLS certificate verification, for testing against a self-signed
listener only — don't use it against a real deployment.

Just like local pairing, this opens a browser window for you to sign into
Google — it happens right here, on this device, which is the whole point of
client mode: the daemon's host never touches your Google account, only the
resulting session material, sent over the network after you've signed in.

The rest of the flow looks identical to local pairing: an emoji to tap on the
phone, then a wait for confirmation. Behind the scenes, the daemon relays the
emoji, waits for the phone, verifies the session by reconnecting, and only
then reports success back to your `pair --remote` — the same guarantee local
pairing gives, just over the network instead of a shell on the daemon's host.
See [SOLUTION.md § Client-mode pairing](SOLUTION.md#client-mode-pairing)
for the design reasoning.

### Checking your own status

```bash
message-gateway status NAME
```

reports whether a session is stored, which phone it's paired with, and how
many conversations are currently bridged for you. Add `--offline` to skip the
network round-trip and only report what's on disk. If you haven't paired
yet, this reports `NOT PAIRED — run "message-gateway pair NAME"` instead.

Note that `status` (like local pairing) needs to run wherever the
configuration file is — normally the daemon's host, regardless of which
pairing mode you used to get paired in the first place.

### Unpairing

```bash
message-gateway logout NAME
```

revokes the pairing on the phone and deletes the stored session. If the
pairing was already revoked from the phone (or the session is too broken to
connect with), add `--local-only` to just delete the stored session without
attempting to reach the phone first. Either way, your Mattermost threads and
message history are left alone — re-pairing the same phone picks them back
up.

### Fallback: manual cookies (headless / no-browser environments)

This is **not** the recommended way to pair — it exists for machines where
`pair` can't open a browser at all: a headless server, an SSH-only box with
no display, or a scripted/automated pairing pipeline. If you can run `pair`
on a machine with a screen, use the default flow above instead.

The fallback supplies the same Google account cookies `pair` would otherwise
capture for you, by hand:

1. Sign into `https://messages.google.com/web` in a **private** browser
   window, on any device — it doesn't need to be the machine `pair` runs on.
2. Open devtools and copy the `SID`, `HSID`, `SSID`, `OSID`, `APISID` and
   `SAPISID` cookies (and `__Secure-1PSIDTS`, if present) into a JSON file:

   ```json
   {"SID": "...", "HSID": "...", "SSID": "...", "OSID": "...",
    "APISID": "...", "SAPISID": "...", "__Secure-1PSIDTS": "..."}
   ```

   Treat this file as a bearer credential for your Google account — delete it
   once pairing succeeds, and keep it out of shell history or anywhere it'd
   get logged.

Then reach `pair` with that JSON in one of three equivalent ways:

- `message-gateway pair NAME --cookies-file cookies.json`
- `message-gateway pair NAME < cookies.json` (piping it to stdin — `pair`
  treats any non-interactive stdin as this fallback automatically, no flag
  needed)
- `message-gateway pair NAME --no-browser` and paste the JSON when prompted —
  useful when stdin is an interactive terminal but you still don't want a
  browser to launch

This works with `--remote` client-mode pairing too: add
`--cookies-file`/`--no-browser` to a `pair NAME --remote ...` invocation
exactly as shown above, and everything past cookie acquisition proceeds
identically to the default flow — same emoji prompt, same verification step.
