// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:01:11
// Original filename: src/internal/gmessages/media.go

package gmessages

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
)

// Download fetches an attachment's bytes.
//
// It prefers the full-size media and falls back to the thumbnail, which is all
// that exists for an MMS the phone has not downloaded yet. The returned
// isThumbnail says which one came back, so the post can say so rather than
// quietly showing a low-resolution image.
func (c *Client) Download(ctx context.Context, media Media) (data []byte, isThumbnail bool, err error) {
	if media.Pending() {
		return nil, false, fmt.Errorf("attachment %q is not available yet: the phone has not downloaded it", media.Name)
	}

	if media.ID != "" {
		data, err = c.gm.DownloadMedia(media.ID, media.DecryptionKey)
		if err == nil {
			return data, false, nil
		}
		if media.ThumbnailID == "" {
			return nil, false, fmt.Errorf("downloading attachment %q: %w", media.Name, err)
		}
		c.log.Warn("falling back to the thumbnail of an attachment",
			"name", media.Name, "error", err)
	}

	data, err = c.gm.DownloadMedia(media.ThumbnailID, media.ThumbnailDecryptionKey)
	if err != nil {
		return nil, false, fmt.Errorf("downloading the thumbnail of attachment %q: %w", media.Name, err)
	}
	return data, true, nil
}

// RequestFullSize asks the phone to download an MMS attachment it has not
// fetched yet. The attachment does not arrive in the reply: the phone sends an
// updated message event once it has it.
func (c *Client) RequestFullSize(ctx context.Context, messageID, actionMessageID string) error {
	if _, err := c.gm.GetFullSizeImage(ctx, messageID, actionMessageID); err != nil {
		return fmt.Errorf("asking the phone for the full-size attachment of %s: %w", messageID, err)
	}
	return nil
}

// FileName returns a usable file name for an attachment. Google Messages
// often supplies a name with no extension, or none at all, and Mattermost
// renders a file better when its name matches its type.
func (m Media) FileName() string {
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = "attachment"
	}
	name = filepath.Base(name)

	if filepath.Ext(name) != "" {
		return name
	}
	if extensions, err := mime.ExtensionsByType(m.MimeType); err == nil && len(extensions) > 0 {
		return name + extensions[0]
	}
	return name
}
