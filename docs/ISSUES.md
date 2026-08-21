# Known issues and limitations

## Blockers

- [ ] **Nothing has been exercised against a real phone or a real Mattermost
      server.** Everything compiles, is unit-tested where it can be tested
      without a network, and the CLI runs end to end against a configuration
      file. The Google Messages protocol paths run entirely through `libgm` and
      have not been validated live.

## Design limits, accepted

- [ ] `libgm` implements a reverse-engineered protocol. Google can change
      authentication, pairing, protobuf formats or endpoints at any time, and
      the bridge breaks until `libgm` catches up. This is the project's main
      maintenance risk — see [SOLUTION.md](SOLUTION.md) §Risks.
- [ ] Routing is decided when a conversation is **first** bridged. Changing a
      rule afterwards does not move the existing thread, because that would
      strand its history in the old channel.
- [ ] `thread_per_conversation` is global rather than per rule. With it off, a
      channel holding more than one conversation cannot be replied to at all:
      the daemon refuses to guess which conversation a reply meant.
- [ ] Replayed messages the bridge has no record of (`IsOld`) are dropped rather
      than posted. Without this, a first run would post the phone's entire
      backlog. Deliberate history is available through
      `gmessages.backfill_count`.
- [ ] Media is buffered fully in memory on the way through, in both directions.
      Fine for photos and MMS; a large RCS video will show up as a memory spike.
- [ ] The `encoded:` secret scheme uses `helperFunctions`' AES-256-CFB, which is
      unauthenticated: a wrong passphrase yields garbage rather than an error.
      The session store catches this by parsing the result as JSON; an
      arbitrary token would not be caught. `vault:` has no such problem.
- [ ] `SIMPayload` for outgoing messages comes from the conversation, falling
      back to the phone's first configured SIM. On a dual-SIM phone whose
      conversations do not carry a SIM card, outgoing messages may go out on the
      wrong SIM.
- [ ] Only QR pairing is implemented. Google-account pairing needs browser
      cookies.

## Untested paths

- [ ] Group RCS: participant changes, group renames, per-sender attribution.
      The storage model does not assume one number per conversation, but the
      behaviour has not been observed.
- [ ] An MMS the phone has not downloaded yet is posted with a note rather than
      the attachment. `RequestFullSize` exists to ask the phone for it and is
      not wired into the inbound path.
- [ ] Reply-to mapping from Mattermost. In thread mode every message is a reply
      to the conversation's root post, so only a reply to a *bridged message*
      that is itself a thread root maps onto an RCS reply. In practice most
      Mattermost replies will send as plain messages.
