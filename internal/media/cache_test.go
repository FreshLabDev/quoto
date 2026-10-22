// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/domain"
)

func cacheEntryRow(id int64, kind, uniqueID, fileID, sha, phash string) cacheRow {
	return cacheRow{
		CacheEntry:   CacheEntry{ID: id, Description: "row " + string(rune('0'+id)), SHA256: sha, Phash: phash},
		Kind:         kind,
		FileUniqueID: uniqueID,
		FileID:       fileID,
	}
}

// tests/test_media.py::test_find_cache_hits_file_unique_id,
// ::test_find_cache_hits_sha256_after_id_miss and
// ::test_find_cache_hits_phash_with_distance_five (MED-04, MED-05), and doc §6
// "Lookup order": file_unique_id, file_id, sha256, phash (distance <= 5), each
// step inside the kind. The calls the store saw are the order.
func TestCacheLookupOrder(t *testing.T) {
	const kind = domain.ContentPhoto
	src := domain.MediaSource{Kind: kind, FileID: "FID", FileUniqueID: "UID"}
	zero := "0000000000000000"
	sha := strings.Repeat("e", 64)

	tests := []struct {
		name  string
		rows  []cacheRow
		src   domain.MediaSource
		sha   string
		phash string
		// what comes back, and the store calls that led to it
		hit   int64
		calls []string
	}{
		{
			name: "unique id wins over everything",
			rows: []cacheRow{
				cacheEntryRow(1, kind, "", "FID", sha, zero),
				cacheEntryRow(2, kind, "UID", "", "", ""),
			},
			src: src, sha: sha, phash: zero,
			hit:   2,
			calls: []string{"find file_unique_id=UID kind=photo"},
		},
		{
			name: "file id after a unique id miss",
			rows: []cacheRow{cacheEntryRow(1, kind, "other", "FID", sha, zero)},
			src:  src, sha: sha, phash: zero,
			hit:   1,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo"},
		},
		{
			name: "sha256 after both ids miss",
			rows: []cacheRow{cacheEntryRow(1, kind, "x", "y", sha, "ffffffffffffffff")},
			src:  src, sha: sha, phash: zero,
			hit: 1,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo"},
		},
		{
			name: "phash last, at distance five",
			rows: []cacheRow{cacheEntryRow(1, kind, "x", "y", "no", "000000000000001f")},
			src:  src, sha: sha, phash: zero,
			hit: 1,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo", "phashes kind=photo", "cache 1"},
		},
		{
			name: "phash at distance six is a miss",
			rows: []cacheRow{cacheEntryRow(1, kind, "x", "y", "no", "000000000000003f")},
			src:  src, sha: sha, phash: zero,
			hit: 0,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo", "phashes kind=photo"},
		},
		{
			name: "the closest look-alike wins, not the first",
			rows: []cacheRow{
				cacheEntryRow(1, kind, "x", "y", "n1", "000000000000001f"), // 5
				cacheEntryRow(2, kind, "x", "y", "n2", "0000000000000003"), // 2
				cacheEntryRow(3, kind, "x", "y", "n3", "0000000000000005"), // 2, but a higher id
			},
			src: src, sha: sha, phash: zero,
			hit: 2,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo", "phashes kind=photo", "cache 2"},
		},
		{
			name: "video hashes: the best pair counts",
			rows: []cacheRow{cacheEntryRow(1, kind, "x", "y", "no", "ffffffffffffffff:0000000000000001")},
			src:  src, sha: sha, phash: "aaaaaaaaaaaaaaaa:0000000000000000",
			hit: 1,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo", "phashes kind=photo", "cache 1"},
		},
		{
			name: "a file without ids goes straight to the hash",
			rows: []cacheRow{cacheEntryRow(1, kind, "x", "y", sha, "")},
			src:  domain.MediaSource{Kind: kind}, sha: sha, phash: "",
			hit:   1,
			calls: []string{"find sha256=" + sha + " kind=photo"},
		},
		{
			name: "no phash, no phash query (audio)",
			rows: []cacheRow{cacheEntryRow(1, kind, "x", "y", "no", zero)},
			src:  src, sha: sha, phash: "",
			hit: 0,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo"},
		},
		{
			name: "another kind is never a hit",
			rows: []cacheRow{cacheEntryRow(1, domain.ContentVideo, "UID", "FID", sha, zero)},
			src:  src, sha: sha, phash: zero,
			hit: 0,
			calls: []string{"find file_unique_id=UID kind=photo", "find file_id=FID kind=photo",
				"find sha256=" + sha + " kind=photo", "phashes kind=photo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.store.cache = tt.rows
			ctx := context.Background()

			c, ok, err := r.p.findByIDs(ctx, kind, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				c, ok, err = r.p.findByContent(ctx, kind, tt.sha, tt.phash)
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := r.store.callLog(); !reflect.DeepEqual(got, tt.calls) {
				t.Fatalf("store calls\n got %q\nwant %q", got, tt.calls)
			}
			if tt.hit == 0 && ok || tt.hit != 0 && (!ok || c.ID != tt.hit) {
				t.Fatalf("hit = (%+v, %t), want id %d", c, ok, tt.hit)
			}
		})
	}
}

// A store error ends the lookup: the pipeline must not go on to download and pay
// for a model call on a cache it could not read.
func TestCacheErrorStopsTheJob(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	r.store.cacheErr = errors.New("db down")

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaFailed || !strings.Contains(res.Error, "cache lookup") {
		t.Fatalf("result %+v", res)
	}
	if len(r.files.getCalls) != 0 || r.desc.count() != 0 {
		t.Fatal("download or model call after a cache error")
	}
}

// Through the whole job, a miss asks in the order of doc §6 and the id steps are
// not asked twice (doc §7.1: "the second id lookup in _find_cache" is gone).
func TestJobLookupSequence(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	r.store.cache = []cacheRow{cacheEntryRow(1, domain.ContentPhoto, "x", "y", "n", "ffffffffffffffff")}

	r.do(1)

	var lookups []string
	for _, c := range r.store.callLog() {
		if strings.HasPrefix(c, "find ") || strings.HasPrefix(c, "phashes ") {
			if i := strings.Index(c, "="); i > 0 && strings.HasPrefix(c, "find sha256") {
				c = c[:i+1] + "<sha>" + c[strings.Index(c, " kind"):]
			}
			lookups = append(lookups, c)
		}
	}
	want := []string{
		"find file_unique_id=u-a kind=photo",
		"find file_id=f-a kind=photo",
		"find sha256=<sha> kind=photo",
		"phashes kind=photo",
	}
	if !reflect.DeepEqual(lookups, want) {
		t.Fatalf("lookups\n got %q\nwant %q", lookups, want)
	}
}

func TestNearest(t *testing.T) {
	rows := []PhashRow{{ID: 5, Phash: "000000000000000f"}, {ID: 2, Phash: "000000000000000f"}, {ID: 9, Phash: "0000000000000003"}}
	if id, ok := nearest("0000000000000000", rows); !ok || id != 9 {
		t.Fatalf("nearest = %d, %t", id, ok)
	}
	if id, ok := nearest("0000000000000000", rows[:2]); !ok || id != 2 {
		t.Fatalf("a tie goes to the lowest id, got %d, %t", id, ok)
	}
	if _, ok := nearest("ffffffffffffffff", rows); ok {
		t.Fatal("nothing is within 5 bits")
	}
	if _, ok := nearest("0000000000000000", nil); ok {
		t.Fatal("no rows, no hit")
	}
	if _, ok := nearest("zz", rows); ok {
		t.Fatal("an unreadable hash matches nothing")
	}
}
