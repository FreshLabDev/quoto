// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// The cache is searched in a fixed order, the first hit wins: Telegram's stable
// file id, the file id, the hash of the normalized bytes, and last a perceptual
// hash within phashMaxDistance bits. Every step stays inside the current prompt
// version and the analysis kind, so a description written by another prompt, or
// for another kind of file, is never reused.
//
// The two id steps run before the download. The other two need the normalized
// file; the id steps are not repeated for it (they already missed).

// findByIDs is the first half of the search.
func (p *Pipeline) findByIDs(ctx context.Context, kind string, src domain.MediaSource) (CacheEntry, bool, error) {
	for _, step := range []struct {
		by    CacheKey
		value string
	}{
		{CacheByFileUniqueID, src.FileUniqueID},
		{CacheByFileID, src.FileID},
	} {
		if step.value == "" {
			continue
		}
		if c, ok, err := p.store.FindCache(ctx, step.by, step.value, kind); err != nil || ok {
			return c, ok, err
		}
	}
	return CacheEntry{}, false, nil
}

// findByContent is the second half: the hash of the file, then the look-alike.
func (p *Pipeline) findByContent(ctx context.Context, kind, sha256, phash string) (CacheEntry, bool, error) {
	if sha256 != "" {
		if c, ok, err := p.store.FindCache(ctx, CacheBySHA256, sha256, kind); err != nil || ok {
			return c, ok, err
		}
	}
	if phash == "" {
		return CacheEntry{}, false, nil
	}
	rows, err := p.store.PhashCandidates(ctx, kind)
	if err != nil {
		return CacheEntry{}, false, err
	}
	id, ok := nearest(phash, rows)
	if !ok {
		return CacheEntry{}, false, nil
	}
	return p.store.CacheByID(ctx, id)
}

// nearest picks the row closest to phash within phashMaxDistance. Python took
// the first row that was close enough, in an order the query did not define;
// the closest one, lowest id on a tie, gives the same answer every time. Only
// ids and hashes are compared here: the description is read for the one row
// that wins.
func nearest(phash string, rows []PhashRow) (id int64, ok bool) {
	best := phashMaxDistance + 1
	for _, r := range rows {
		d := phashDistance(phash, r.Phash)
		if d < best || (d == best && ok && r.ID < id) {
			best, id, ok = d, r.ID, true
		}
	}
	return id, ok
}
