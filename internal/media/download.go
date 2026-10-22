// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"path/filepath"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// Files is the part of *tg.Client the pipeline uses: getFile, then a download
// straight to disk with a size limit. Nothing is read into memory.
type Files interface {
	GetFile(ctx context.Context, fileID string) (tg.File, error)
	DownloadToFile(ctx context.Context, filePath, dst string, maxBytes int64) error
}

// The bot passes its client as Files; the compiler checks the signatures here
// instead of leaving the first build of cmd/bot to find a mismatch.
var _ Files = (*tg.Client)(nil)

// download fetches the file of src into dir and returns its path. Failures come
// back classified: a *domain.MediaRetryableError for what a later attempt can
// get past (a network error, a flood wait; decision M1), an error wrapping
// tg.ErrFileTooLarge for a file over the limit (decision M4, never retried), and
// anything else is final.
func (p *Pipeline) download(ctx context.Context, src domain.MediaSource, dir string) (string, error) {
	if src.FileID == "" {
		return "", errors.New("telegram file_id is missing")
	}
	// The size in the message spares a getFile call for a file that will not be
	// taken anyway.
	if src.FileSize > maxDownloadBytes {
		return "", fmt.Errorf("%w: the message says %d bytes, the limit is %d", tg.ErrFileTooLarge, src.FileSize, maxDownloadBytes)
	}
	file, err := p.files.GetFile(ctx, src.FileID)
	if err != nil {
		return "", classifyDownload("get file", err)
	}
	if file.FileSize > maxDownloadBytes {
		return "", fmt.Errorf("%w: telegram says %d bytes, the limit is %d", tg.ErrFileTooLarge, file.FileSize, maxDownloadBytes)
	}
	suffix := suffixForMime(src.MimeType)
	if suffix == "" {
		suffix = path.Ext(file.FilePath)
	}
	if suffix == "" {
		suffix = ".bin"
	}
	dst := filepath.Join(dir, "telegram"+suffix)
	if err := p.files.DownloadToFile(ctx, file.FilePath, dst, maxDownloadBytes); err != nil {
		return "", classifyDownload("download file", err)
	}
	return dst, nil
}

// classifyDownload sorts a failure of getFile or the download. The tg client
// already redacts the bot token from what it returns.
func classifyDownload(op string, err error) error {
	if errors.Is(err, tg.ErrFileTooLarge) {
		return fmt.Errorf("%s: %w", op, err)
	}
	var api *tg.APIError
	if errors.As(err, &api) {
		switch {
		// The cloud Bot API answers a getFile above 20 MB with 400 and this
		// text; tg has no separate marker for it (phase0/09-tg-gaps.md 3.4).
		case strings.Contains(strings.ToLower(api.Description), "too big"):
			return fmt.Errorf("%s: %w: %s", op, tg.ErrFileTooLarge, api.Description)
		case tg.IsTooManyRequests(err):
			return &domain.MediaRetryableError{Err: fmt.Errorf("%s: %w", op, err), RetryAfter: tg.RetryAfter(err), CountsAsAttempt: true}
		case api.StatusCode >= 500:
			return &domain.MediaRetryableError{Err: fmt.Errorf("%s: %w", op, err), CountsAsAttempt: true}
		}
		return fmt.Errorf("%s: %w", op, err)
	}
	if isNetworkError(err) {
		return &domain.MediaRetryableError{Err: fmt.Errorf("%s: %w", op, err), CountsAsAttempt: true}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isNetworkError recognises a failure of the connection itself. tg wraps the
// errors of net/http in a type it does not export, but keeps the cause in the
// chain, and its message always starts with the text below.
func isNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	return strings.Contains(err.Error(), "telegram transport failed")
}

// suffixForMime names the downloaded file after what it should be, so that the
// decoders and ffmpeg see a familiar extension. They sniff the content anyway.
func suffixForMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "application/x-tgsticker":
		return ".tgs"
	}
	return ""
}
