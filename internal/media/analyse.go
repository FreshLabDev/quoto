// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// analyse ports _process_media_source, steps 1 to 7 of phase0/02-media.md 1.3,
// for a row that is already claimed. It returns the outcome to store; an error
// is a failure for failure() to sort into "try later" and "never".
func (p *Pipeline) analyse(ctx context.Context, item Item, src domain.MediaSource) (Result, error) {
	meta := domain.MetadataDescription(src)
	closed := Result{Status: domain.MediaDisabled, Description: meta}

	// Step 1. The group's own switch, and (decision R4) the agreement: a group
	// that has not accepted a version that counts sends nothing anywhere.
	if !p.groupOpen(ctx, item.MessageDBID) {
		return closed, nil
	}
	// Step 2. The global switch, and files no model can read.
	if !p.cfg.AnalysisEnabled || !src.SupportsAnalysis {
		return Result{Status: domain.MediaUnsupported, Description: meta}, nil
	}

	kind := domain.AnalysisKind(src)

	// Step 3. A file seen before costs nothing, not even a download.
	if c, ok, err := p.findByIDs(ctx, kind, src); err != nil {
		return Result{}, fmt.Errorf("cache lookup: %w", err)
	} else if ok {
		return cachedResult(c, "", ""), nil
	}

	// Step 4. One directory per file, gone when the job ends.
	dir, err := os.MkdirTemp(p.cfg.TempDir, "quoto-media-")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	raw, err := p.download(ctx, src, dir)
	if err != nil {
		return Result{}, err
	}
	norm, err := p.normalize(ctx, src, raw, dir)
	if err != nil {
		return Result{}, err
	}
	if c, ok, err := p.findByContent(ctx, kind, norm.SHA256, norm.Phash); err != nil {
		return Result{}, fmt.Errorf("cache lookup: %w", err)
	} else if ok {
		return cachedResult(c, norm.SHA256, norm.Phash), nil
	}

	// Step 5. The switch may have been turned off, or the agreement withdrawn,
	// while the file was downloading. This is the last look before the model.
	if !p.groupOpen(ctx, item.MessageDBID) {
		return closed, nil
	}

	// Steps 6 and 7.
	d, err := p.describe(ctx, src, kind, norm)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Status:      domain.MediaAnalyzed,
		Description: d.Text,
		CacheID:     d.Cache.ID,
		SHA256:      firstNonEmpty(norm.SHA256, d.Cache.SHA256),
		Phash:       firstNonEmpty(norm.Phash, d.Cache.Phash),
	}, nil
}

// groupOpen says whether the file's group lets its media be analysed. Anything
// short of a clear yes is a no: a database error closes the gate, as the Python
// version did, and so does a group that has not accepted the agreement.
func (p *Pipeline) groupOpen(ctx context.Context, messageDBID int64) bool {
	g, found, err := p.store.Group(ctx, messageDBID)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			p.log.Error("cannot verify the group's media settings; media analysis stays off for this file",
				"message_db_id", messageDBID, "error", err)
		}
		return false
	case !found:
		return false
	case !g.MediaAnalysisEnabled():
		p.log.Debug("media analysis is switched off for the group", "message_db_id", messageDBID)
		return false
	case !g.AgreementAccepted():
		p.log.Debug("the group has not accepted the agreement; its media is not sent anywhere", "message_db_id", messageDBID)
		return false
	}
	return true
}

// cachedResult is the outcome of a cache hit. Hashes computed for the file win
// over the cache row's own.
func cachedResult(c CacheEntry, sha256, phash string) Result {
	return Result{
		Status:      domain.MediaCached,
		Description: c.Description,
		CacheID:     c.ID,
		SHA256:      firstNonEmpty(sha256, c.SHA256),
		Phash:       firstNonEmpty(phash, c.Phash),
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// described is what one model call produced: the text for this message and the
// cache row that now holds it.
type described struct {
	Text  string
	Cache CacheEntry
}

// describe asks the model once per file. Two messages with the same file that
// reach this point together share one call: the second waits for the first and
// gets its answer. The key is the cache's own, kind and Telegram's unique file
// id; a file without one is told apart by the hash of its normalized bytes.
func (p *Pipeline) describe(ctx context.Context, src domain.MediaSource, kind string, norm Normalized) (described, error) {
	key := kind + "|" + src.FileUniqueID
	if src.FileUniqueID == "" {
		key = kind + "|sha256:" + norm.SHA256
	}
	call := func() (any, error) {
		desc, err := p.describer.DescribeMedia(ctx, domain.MediaDescribeRequest{
			Path:     norm.Path,
			MimeType: norm.Mime,
			Kind:     kind,
		})
		if err != nil {
			return nil, err
		}
		cache, err := p.store.CreateCache(ctx, NewCache{
			Kind:             kind,
			FileUniqueID:     src.FileUniqueID,
			FileID:           src.FileID,
			SHA256:           norm.SHA256,
			Phash:            norm.Phash,
			Description:      desc.Text,
			Model:            desc.ActualModel,
			PromptTokens:     desc.PromptTokens,
			CompletionTokens: desc.CompletionTokens,
			TotalTokens:      desc.TotalTokens,
		})
		if err != nil {
			return nil, fmt.Errorf("store cache row: %w", err)
		}
		return described{Text: desc.Text, Cache: cache}, nil
	}

	v, err, _ := p.flight.Do(key, call)
	// The call runs on the context of whoever started it. If that job timed out
	// or was cancelled and this one was not, the failure is not ours: go again.
	if err != nil && ctx.Err() == nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		v, err, _ = p.flight.Do(key, call)
	}
	if err != nil {
		return described{}, err
	}
	return v.(described), nil
}
