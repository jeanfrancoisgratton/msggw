# Creating a Discord bot for msggw

This is a from-scratch, click-by-click walkthrough for someone who has never
created a Discord bot before. It covers only what `msg-gw` actually needs —
there is no slash-command setup, no application-commands registration, none
of the things a "how to make a Discord bot" tutorial usually spends most of
its time on, because `msg-gw` only ever reads and sends plain messages.

If you've done this before, the short version is: one application, one bot
user, the **Message Content** privileged intent turned on, an invite link
with **View Channels / Send Messages / Read Message History / Attach
Files**, and the token dropped into a file `discord.token_ref` points at.
Everything below is that, spelled out.

---

## What you're creating, and why

Discord has no equivalent of "sign in as yourself" for a bridge like this.
Instead, you register a small application on Discord's own site, which gets
you a **bot user** with its own name, avatar, and — critically — its own
**token**, a long secret string that lets `msg-gw` log in as that bot over
Discord's API. `msg-gw` is one shared bot for the whole daemon, not one per
person (see `config.DiscordConfig` and `docs/discord-adapter-design.md`), so
you only ever do this once per deployment, not once per user.

You'll need a Discord account, but you do **not** need to own a Discord
server ("guild") to test with — a bot can be DMed directly, which is the
easiest way to try the bridge for the first time. If you do want to test a
guild channel too, you'll need permission to invite bots to at least one
server (your own test server is easiest).

---

## 1. Create the application

1. Go to the [Discord Developer Portal](https://discord.com/developers/applications)
   and log in with your normal Discord account.
2. Click **New Application**, top right.
3. Give it a name — this is what shows up as the bot's default username
   (you can rename it later), e.g. `msggw`. Accept the terms and click
   **Create**.

You're now on the application's **General Information** page. The
**Application ID** here is not something `msg-gw` needs; you can ignore it.

## 2. Turn it into a bot and get its token

1. In the left sidebar, click **Bot**.
2. A bot user is created automatically under the application (older guides
   describe a separate "Add Bot" button — current Discord no longer needs
   that click). Give it a username here if you want something different
   from the application name.
3. Scroll down to **Privileged Gateway Intents** and turn on
   **Message Content Intent**. This is the one that matters: without it,
   Discord delivers messages to the bot with their text stripped out, and
   `msg-gw` would bridge every message as empty. You do **not** need
   **Presence Intent** or **Server Members Intent** — `msg-gw` doesn't use
   either (a guild channel's participant list is best-effort in this v1,
   built from messages actually seen rather than a member-list fetch — see
   `docs/discord-adapter-design.md` §4).
4. Click **Save Changes** if the page doesn't save automatically.
5. Near the top of the same page, under the bot's username, click
   **Reset Token** (it may say **Reset Token** even the first time — that's
   normal, it just means "generate one"). Confirm, and copy the token
   that appears. **This is the only time Discord shows it to you in full;**
   if you navigate away without copying it, you'll have to reset it again
   (which invalidates the old one).

Keep this token somewhere safe for the next step. Treat it exactly like a
password — anyone with it can log in as your bot.

## 3. Store the token where `msg-gw` can read it

`msg-gw` never takes a credential as a plain value in `config.json` — see
[Secret references](CONFIGURATION.md#secret-references). For a Discord bot
token, the simplest option is a plain file:

```bash
sudo install -d -m 0700 /etc/msggw
sudo tee /etc/msggw/discord.token > /dev/null <<< 'paste-the-token-here'
sudo chmod 0600 /etc/msggw/discord.token
```

That file's path, prefixed with `file:`, is what you'll write as
`discord.token_ref` in the next step: `file:/etc/msggw/discord.token`. An
environment variable (`env:DISCORD_TOKEN`) or Vault (`vault:secrets/msggw#discord_token`)
both work too, on the same terms as the Mattermost bot token.

**Common mistake:** paste only the token itself, not `Bot <token>` — `msg-gw`
adds the `Bot ` prefix Discord's API expects internally
(`internal/discord/client.go`). If you paste a token that already starts
with `Bot `, the connection will fail to authenticate.

## 4. Invite the bot to a server (optional, but needed to test a guild channel)

Skip this step if you only want to test via direct messages — a bot can
receive a DM from anyone without being a member of any server first (as long
as you and the bot share at least one server, *or* you message it from a
context Discord allows DMs from; the simplest way to guarantee this is to
still do this step even for a DM-only test, using your own personal test
server).

1. Back on the **Bot** page (or the **OAuth2** page), find the
   **OAuth2 URL Generator** (**OAuth2 → URL Generator** in the sidebar).
2. Under **Scopes**, check **bot**.
3. Under **Bot Permissions**, which appears once you check *bot*, check:
   - **View Channels**
   - **Send Messages**
   - **Read Message History**
   - **Attach Files**

   (**Embed Links** is a reasonable extra if you want nicer link previews
   in Mattermost-originated messages, but not required.) Do **not** grant
   **Administrator** — this bot only ever needs to see and post in the
   specific channels you route to it.
4. Copy the generated URL at the bottom of the page, paste it into a
   browser, pick the server to add it to (you need **Manage Server**
   permission on that server yourself), and click **Authorize**.
5. The bot now appears in that server's member list, shown as offline —
   it stays offline until `msg-gw` actually connects (step 6).

## 5. Configure `msg-gw`

In `config.json` (see `msg-gw config sample` and `src/internal/config/config.sample.json`
for a full worked example), add a top-level `discord` block:

```json
"discord": {
  "token_ref": "file:/etc/msggw/discord.token",
  "routing": {
    "default_direct": { "type": "channel", "team": "myteam", "channel": "discord-dms" },
    "default_group": { "type": "channel", "team": "myteam", "channel": "discord" },
    "thread_per_conversation": true,
    "join_channels": true
  }
}
```

- `token_ref` — the secret reference from step 3. An empty/missing
  `token_ref` disables Discord entirely; the daemon simply won't start a
  Discord connection.
- `routing.default_direct` — where a DM or group DM lands when no rule
  matches.
- `routing.default_group` — where a guild channel lands when no rule
  matches; left out, it falls back to `default_direct`.
- `routing.rules` — optional, first-match-wins rules keyed on
  `guild_ids`/`channel_ids`/`channel_name_pattern`/`groups_only`/`directs_only`,
  same shape as `users[].routing.rules` but for Discord's own identifiers
  (see `config.sample.json` for a worked rule).
- `join_channels: true` lets the bot create/join the Mattermost channel
  itself rather than requiring you to add it by hand first.

Validate before running:

```bash
msg-gw config check
```

## 6. Run it and confirm the connection

Start (or reload) the daemon, and watch its log for a line like:

```
bridge running backend=discord default_direct=... default_group=... routing_rules=0 threads=true
connected to Discord
```

Back in Discord, the bot should now show **online** wherever you invited it.

## 7. Try it

The fastest end-to-end check needs no server at all:

1. Open a DM with the bot in Discord (find it under a shared server's
   member list, click it, click **Message**) and send it a line of text.
2. Confirm that text shows up as a new post/thread in the Mattermost
   destination `routing.default_direct` points at.
3. Reply to that post in Mattermost, and confirm the reply arrives back in
   the Discord DM.

If you invited the bot to a server, do the same in a channel it can see —
that exercises `routing.default_group` (or a matching rule) instead.

---

## Troubleshooting

- **Messages arrive in Mattermost with no text.** The **Message Content
  Intent** (step 2) isn't turned on, or wasn't saved. Go back and check it.
- **Nothing happens at all when you message the bot in a server channel.**
  The bot can't see that channel — check its permissions there (View
  Channels/Send Messages), or that it was actually invited with the *bot*
  scope and not just added as an OAuth2 application with no bot user.
- **The daemon logs an authentication failure on connect.** The token was
  copied with a `Bot ` prefix already attached, has surrounding whitespace/a
  trailing newline from how it was saved to the file, or was reset in the
  portal since (resetting invalidates the previous token immediately).
- **The bot only sees some of a channel's history/participants.**
  Expected in this v1 — see `docs/discord-adapter-design.md` §4 and §6 for
  what's deliberately not implemented yet (no channel-rename tracking, no
  backfill, best-effort guild-channel participant lists).
