// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:03:28
// Original filename: src/internal/mattermost/media.go

package mattermost

import (
	"context"
	"fmt"
)

// File is an attachment on a Mattermost post.
type File struct {
	ID       string
	Name     string
	MimeType string
	Size     int64
}

// Upload stores a file in a channel and returns its ID, ready to be attached
// to a post. Mattermost requires the upload and the post to name the same
// channel.
func (c *Client) Upload(ctx context.Context, channelID, filename string, data []byte) (string, error) {
	resp, _, err := c.api.UploadFile(ctx, data, channelID, filename)
	if err != nil {
		return "", fmt.Errorf("uploading %s to channel %s: %w", filename, channelID, err)
	}
	if len(resp.FileInfos) == 0 {
		return "", fmt.Errorf("uploading %s to channel %s: Mattermost accepted the file but returned no file info",
			filename, channelID)
	}
	return resp.FileInfos[0].Id, nil
}

// FileInfo describes an attachment without downloading it.
func (c *Client) FileInfo(ctx context.Context, fileID string) (File, error) {
	info, _, err := c.api.GetFileInfo(ctx, fileID)
	if err != nil {
		return File{}, fmt.Errorf("fetching info for file %s: %w", fileID, err)
	}
	return File{
		ID:       info.Id,
		Name:     info.Name,
		MimeType: info.MimeType,
		Size:     info.Size,
	}, nil
}

// Download returns an attachment's bytes, for forwarding to the phone.
func (c *Client) Download(ctx context.Context, fileID string) ([]byte, error) {
	data, _, err := c.api.GetFile(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("downloading file %s: %w", fileID, err)
	}
	return data, nil
}
