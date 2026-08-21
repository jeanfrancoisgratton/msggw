// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:03:06
// Original filename: src/internal/mattermost/channels.go

package mattermost

import (
	"context"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"rcs_gateway/internal/config"
)

// ResolveDestination turns a configured destination into a channel ID the
// daemon can post to, joining the channel first when that is allowed and
// needed.
//
// Resolutions are cached: the answer for a given destination does not change
// while the daemon runs, and re-resolving costs REST calls on the path of
// every message.
func (c *Client) ResolveDestination(ctx context.Context, dest config.Destination) (string, error) {
	key := destinationKey(dest)

	c.mu.RLock()
	cached, ok := c.channels[key]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}

	channelID, err := c.resolve(ctx, dest)
	if err != nil {
		return "", err
	}
	if err := c.ensureMember(ctx, channelID); err != nil {
		return "", err
	}

	c.mu.Lock()
	c.channels[key] = channelID
	c.mu.Unlock()

	c.log.Info("resolved a routing destination",
		"destination", dest.String(), "channel_id", channelID)
	return channelID, nil
}

func (c *Client) resolve(ctx context.Context, dest config.Destination) (string, error) {
	switch dest.Type {
	case config.DestChannelID:
		channel, _, err := c.api.GetChannel(ctx, dest.ChannelID)
		if err != nil {
			return "", fmt.Errorf("looking up channel %s: %w", dest.ChannelID, err)
		}
		return channel.Id, nil

	case config.DestChannel:
		channel, _, err := c.api.GetChannelByNameForTeamName(ctx, dest.Channel, dest.Team, "")
		if err != nil {
			return "", fmt.Errorf("looking up channel ~%s in team %s: %w", dest.Channel, dest.Team, err)
		}
		return channel.Id, nil

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

// ensureMember makes sure the bot can post in a channel.
//
// Direct and group channels make their members members by construction, so
// this only ever does anything for a named channel.
func (c *Client) ensureMember(ctx context.Context, channelID string) error {
	_, _, err := c.api.GetChannelMember(ctx, channelID, c.BotUserID(), "")
	if err == nil {
		return nil
	}

	if !c.cfg.JoinChannels {
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
