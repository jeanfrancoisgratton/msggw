// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/discord.go

package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"msggw/internal/config"
	"msggw/internal/corebridge"
	"msggw/internal/discord"
	"msggw/internal/mattermost"
	"msggw/internal/secrets"
	"msggw/internal/storage"
)

// runDiscord brings up and runs the Discord bridge for the lifetime of ctx.
// Unlike runUser, there is at most one of these per daemon: Discord is one
// shared bot connection, not per-tenant — see config.DiscordConfig's doc
// comment and docs/discord-adapter-design.md.
//
// Every failure here is logged and simply ends this goroutine, the same as
// runUser: Discord failing to come up must not take down GMessages users'
// bridges, or the daemon as a whole.
func runDiscord(ctx context.Context, cfg *config.Config, log *slog.Logger, db *storage.DB, mm *mattermost.Client) {
	log = log.With("backend", "discord")

	token, err := secrets.OpenString(cfg.Discord.TokenRef, cfg.Vault)
	if err != nil {
		log.Error("misconfigured, not starting", "error", fmt.Errorf("discord.token_ref: %w", err))
		return
	}

	dc, err := discord.New(discord.Config{Token: token, Logger: log})
	if err != nil {
		log.Error("could not build the Discord client", "error", err)
		return
	}
	if err := dc.Connect(ctx); err != nil {
		log.Error("could not connect to Discord", "error", err)
		return
	}
	defer dc.Disconnect()

	br, err := corebridge.New("discord", bridgeConfigFromDiscord(cfg.Discord.Routing), log, db, dc.AsTransport(), mm)
	if err != nil {
		log.Error("could not start the bridge", "error", err)
		return
	}

	log.Info("bridge running",
		"default_direct", cfg.Discord.Routing.DefaultDirect.String(),
		"default_group", defaultDiscordGroupLog(cfg.Discord.Routing),
		"routing_rules", len(cfg.Discord.Routing.Rules),
		"threads", cfg.Discord.Routing.ThreadPerConversationEnabled())

	if err := br.Run(ctx); err != nil {
		log.Error("the bridge stopped", "error", err)
	}
}

// defaultDiscordGroupLog renders discord.routing.default_group for a log
// line, mirroring defaultGroupLog for RoutingConfig.
func defaultDiscordGroupLog(r config.DiscordRoutingConfig) string {
	if r.DefaultGroup.Type == "" {
		return "(same as default_direct)"
	}
	return r.DefaultGroup.String()
}

// bridgeConfigFromDiscord translates config.DiscordRoutingConfig into the
// generic corebridge.BridgeConfig every backend's bridge is built from. This
// is the only place Discord's own config field names are known outside
// package config — see corebridge.RulesFromDiscord.
func bridgeConfigFromDiscord(r config.DiscordRoutingConfig) corebridge.BridgeConfig {
	return corebridge.BridgeConfig{
		DefaultDirect:         r.DefaultDirect,
		DefaultGroup:          r.DefaultGroup,
		Rules:                 corebridge.RulesFromDiscord(r.Rules),
		ThreadPerConversation: r.ThreadPerConversationEnabled(),
		JoinChannels:          r.JoinChannels,
	}
}
