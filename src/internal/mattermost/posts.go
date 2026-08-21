// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:03:20
// Original filename: src/internal/mattermost/posts.go

package mattermost

import (
	"context"
	"fmt"

	"github.com/mattermost/mattermost/server/public/model"
)

// PropBridge marks every post the daemon creates. Filtering on the bot's user
// ID already prevents loops, but the property also lets an operator find
// bridged posts, and lets a future release recognise its own posts after the
// bot account is changed.
const PropBridge = "msggw"

// NewPost is a post to create.
type NewPost struct {
	ChannelID string
	// RootID makes the post a reply in an existing thread. Empty creates a new
	// top-level post.
	RootID  string
	Message string
	FileIDs []string
	// Props are merged into the post's properties, on top of the bridge marker.
	Props map[string]any
}

// Post creates a post and returns its ID.
func (c *Client) Post(ctx context.Context, post NewPost) (string, error) {
	props := model.StringInterface{PropBridge: true}
	for k, v := range post.Props {
		props[k] = v
	}

	created, _, err := c.api.CreatePost(ctx, &model.Post{
		ChannelId: post.ChannelID,
		RootId:    post.RootID,
		Message:   post.Message,
		FileIds:   post.FileIDs,
		Props:     props,
	})
	if err != nil {
		return "", fmt.Errorf("creating a post in channel %s: %w", post.ChannelID, err)
	}
	return created.Id, nil
}

// UpdateMessage rewrites an existing post's text, for a message the phone
// later reports as edited or failed.
func (c *Client) UpdateMessage(ctx context.Context, postID, message string) error {
	_, _, err := c.api.PatchPost(ctx, postID, &model.PostPatch{Message: &message})
	if err != nil {
		return fmt.Errorf("updating post %s: %w", postID, err)
	}
	return nil
}

// React adds an emoji reaction to a post, as the bot.
//
// The emoji name is Mattermost's, without colons — "white_check_mark", not
// ":white_check_mark:".
func (c *Client) React(ctx context.Context, postID, emojiName string) error {
	_, _, err := c.api.SaveReaction(ctx, &model.Reaction{
		UserId:    c.BotUserID(),
		PostId:    postID,
		EmojiName: emojiName,
	})
	if err != nil {
		return fmt.Errorf("reacting %s to post %s: %w", emojiName, postID, err)
	}
	return nil
}

// Unreact removes one of the bot's own reactions. A reaction that is not there
// is not an error: the caller usually just wants the end state.
func (c *Client) Unreact(ctx context.Context, postID, emojiName string) error {
	_, err := c.api.DeleteReaction(ctx, &model.Reaction{
		UserId:    c.BotUserID(),
		PostId:    postID,
		EmojiName: emojiName,
	})
	if err != nil {
		return fmt.Errorf("removing reaction %s from post %s: %w", emojiName, postID, err)
	}
	return nil
}

// GetPost fetches a post, used to find the root of a thread a reply landed in.
func (c *Client) GetPost(ctx context.Context, postID string) (Post, error) {
	post, _, err := c.api.GetPost(ctx, postID, "")
	if err != nil {
		return Post{}, fmt.Errorf("fetching post %s: %w", postID, err)
	}
	return convertPost(post), nil
}
