// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:03:06
// Original filename: src/internal/mattermost/channels.go

package mattermost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"msggw/internal/config"
	"msggw/internal/secrets"
)

// ResolveDestination turns a configured destination into a channel ID the
// daemon can post to, joining (or creating) the channel first when
// joinChannels allows it and it is needed. joinChannels is the caller's own
// routing.join_channels — each tenant's own setting governs its own
// destinations, since one shared Client serves every tenant.
//
// Resolutions are cached: the answer for a given destination does not change
// while the daemon runs, and re-resolving costs REST calls on the path of
// every message.
func (c *Client) ResolveDestination(ctx context.Context, dest config.Destination, joinChannels bool) (string, error) {
	key := destinationKey(dest)

	c.mu.RLock()
	cached, ok := c.channels[key]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}

	channelID, err := c.resolve(ctx, dest, joinChannels)
	if err != nil {
		return "", err
	}
	if err := c.ensureMember(ctx, channelID, joinChannels); err != nil {
		return "", err
	}

	c.mu.Lock()
	c.channels[key] = channelID
	c.mu.Unlock()

	c.log.Info("resolved a routing destination",
		"destination", dest.String(), "channel_id", channelID)
	return channelID, nil
}

func (c *Client) resolve(ctx context.Context, dest config.Destination, joinChannels bool) (string, error) {
	dest, err := c.resolveDestinationRefs(dest)
	if err != nil {
		return "", err
	}

	switch dest.Type {
	case config.DestChannelID:
		channel, _, err := c.api.GetChannel(ctx, dest.ChannelID)
		if err != nil {
			return "", fmt.Errorf("looking up channel %s: %w", dest.ChannelID, err)
		}
		return channel.Id, nil

	case config.DestChannel:
		channel, _, err := c.api.GetChannelByNameForTeamName(ctx, dest.Channel, dest.Team, "")
		if err == nil {
			return channel.Id, nil
		}
		if !isNotFound(err) || !joinChannels {
			return "", fmt.Errorf("looking up channel ~%s in team %s: %w", dest.Channel, dest.Team, err)
		}
		return c.createChannel(ctx, dest)

	case config.DestDirect:
		userID, err := c.userID(ctx, dest.User)
		if err != nil {
			return "", err
		}
		channel, _, err := c.api.CreateDirectChannel(ctx, c.BotUserID(), userID)
		if err != nil {
			return "", fmt.Errorf("opening a direct channel with @%s: %w", dest.User, err)
		}
		return channel.Id, nil

	case config.DestGroup:
		// The bot has to be part of the group message it posts into, so its own
		// ID goes in alongside the configured members.
		userIDs := make([]string, 0, len(dest.Users)+1)
		userIDs = append(userIDs, c.BotUserID())
		for _, username := range dest.Users {
			userID, err := c.userID(ctx, username)
			if err != nil {
				return "", err
			}
			userIDs = append(userIDs, userID)
		}
		channel, _, err := c.api.CreateGroupChannel(ctx, userIDs)
		if err != nil {
			return "", fmt.Errorf("opening a group channel with @%s: %w",
				strings.Join(dest.Users, ", @"), err)
		}
		return channel.Id, nil

	default:
		return "", fmt.Errorf("unknown destination type %q", dest.Type)
	}
}

// createChannel creates a named channel that GetChannelByNameForTeamName just
// reported missing. It defaults to private: a group's RCS/SMS history is
// personal content, not something that should default to being visible to
// everyone else on the Mattermost server. The bot creating the channel is
// automatically a member of it, so no separate ensureMember call is needed
// for this path.
func (c *Client) createChannel(ctx context.Context, dest config.Destination) (string, error) {
	team, _, err := c.api.GetTeamByName(ctx, dest.Team, "")
	if err != nil {
		return "", fmt.Errorf("looking up team %s to create channel ~%s: %w", dest.Team, dest.Channel, err)
	}

	channel, _, err := c.api.CreateChannel(ctx, &model.Channel{
		TeamId:      team.Id,
		Name:        dest.Channel,
		DisplayName: dest.Channel,
		Type:        model.ChannelTypePrivate,
	})
	if err != nil {
		return "", fmt.Errorf("creating channel ~%s in team %s: %w", dest.Channel, dest.Team, err)
	}
	c.log.Info("created a missing routing destination channel",
		"channel", dest.Channel, "team", dest.Team, "channel_id", channel.Id)
	return channel.Id, nil
}

// isNotFound reports whether err is a Mattermost 404 — the channel or team
// named simply does not exist yet, as opposed to a real failure (bad
// credentials, the server being unreachable, ...).
func isNotFound(err error) bool {
	var appErr *model.AppError
	return errors.As(err, &appErr) && appErr.StatusCode == http.StatusNotFound
}

// resolveDestinationRefs resolves any of dest's fields that are given as a
// secret reference rather than a plain value, e.g. a team or username kept
// out of the config file behind a vault: reference.
func resolveDestinationRefs(dest config.Destination, vaultCfg secrets.VaultConfig) (config.Destination, error) {
	var err error
	if dest.Team, err = secrets.MaybeResolve(dest.Team, vaultCfg); err != nil {
		return dest, fmt.Errorf("destination team: %w", err)
	}
	if dest.Channel, err = secrets.MaybeResolve(dest.Channel, vaultCfg); err != nil {
		return dest, fmt.Errorf("destination channel: %w", err)
	}
	if dest.ChannelID, err = secrets.MaybeResolve(dest.ChannelID, vaultCfg); err != nil {
		return dest, fmt.Errorf("destination channel_id: %w", err)
	}
	if dest.User, err = secrets.MaybeResolve(dest.User, vaultCfg); err != nil {
		return dest, fmt.Errorf("destination user: %w", err)
	}
	if len(dest.Users) > 0 {
		// dest.Users shares its backing array with the caller's config, so it
		// is resolved into a fresh slice rather than mutated in place.
		users := make([]string, len(dest.Users))
		for i, username := range dest.Users {
			if users[i], err = secrets.MaybeResolve(username, vaultCfg); err != nil {
				return dest, fmt.Errorf("destination users[%d]: %w", i, err)
			}
		}
		dest.Users = users
	}
	return dest, nil
}

func (c *Client) resolveDestinationRefs(dest config.Destination) (config.Destination, error) {
	return resolveDestinationRefs(dest, c.cfg.Vault)
}

// ensureMember makes sure the bot can post in a channel.
//
// Direct and group channels make their members members by construction, so
// this only ever does anything for a named channel.
func (c *Client) ensureMember(ctx context.Context, channelID string, joinChannels bool) error {
	_, _, err := c.api.GetChannelMember(ctx, channelID, c.BotUserID(), "")
	if err == nil {
		return nil
	}

	if !joinChannels {
		return fmt.Errorf("the bot @%s is not a member of channel %s, and routing.join_channels is off: add it to the channel, or turn that setting on",
			c.BotUsername(), channelID)
	}

	if _, _, err := c.api.AddChannelMember(ctx, channelID, c.BotUserID()); err != nil {
		return fmt.Errorf("adding the bot @%s to channel %s: %w", c.BotUsername(), channelID, err)
	}
	c.log.Info("added the bot to a channel", "channel_id", channelID, "bot", c.BotUsername())
	return nil
}

// userID resolves a username to its ID, with a cache.
func (c *Client) userID(ctx context.Context, username string) (string, error) {
	username = strings.TrimPrefix(username, "@")

	c.mu.RLock()
	cached, ok := c.users[username]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}

	user, _, err := c.api.GetUserByUsername(ctx, username, "")
	if err != nil {
		return "", fmt.Errorf("looking up Mattermost user @%s: %w", username, err)
	}

	c.mu.Lock()
	c.users[username] = user.Id
	c.mu.Unlock()
	return user.Id, nil
}

// ChannelIsDirect reports whether a channel is a direct or group message,
// which changes how the bridge names a conversation in it.
func (c *Client) ChannelIsDirect(ctx context.Context, channelID string) (bool, error) {
	channel, _, err := c.api.GetChannel(ctx, channelID)
	if err != nil {
		return false, fmt.Errorf("looking up channel %s: %w", channelID, err)
	}
	return channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup, nil
}

// destinationKey is the cache key for a destination. It has to distinguish
// every field that takes part in resolution, or two different destinations
// would share an entry.
func destinationKey(dest config.Destination) string {
	return strings.Join([]string{
		dest.Type, dest.Team, dest.Channel, dest.ChannelID, dest.User,
		strings.Join(dest.Users, ","),
	}, "\x00")
}
