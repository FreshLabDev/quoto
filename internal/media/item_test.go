// SPDX-License-Identifier: Apache-2.0

package media

import (
	"testing"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// tests/test_media.py::test_source_from_media_item_reconstructs_retry_source and
// ::test_source_from_tgs_sticker_item_is_unsupported (MED-03), and the table of
// _supports_analysis in phase0/02-media.md 1.2: a row is turned back into a
// MediaSource and SupportsAnalysis is recomputed, not stored.
func TestItemSource(t *testing.T) {
	tests := []struct {
		name     string
		item     Item
		supports bool
	}{
		{"photo", Item{Kind: domain.ContentPhoto, MimeType: "image/jpeg"}, true},
		{"image document", Item{Kind: domain.ContentImage, MimeType: "image/png"}, true},
		{"video", Item{Kind: domain.ContentVideo, MimeType: "video/mp4"}, true},
		{"animation", Item{Kind: domain.ContentAnimation, MimeType: "video/mp4"}, true},
		{"video note", Item{Kind: domain.ContentVideoNote, MimeType: "video/mp4"}, true},
		{"voice", Item{Kind: domain.ContentVoice, MimeType: "audio/ogg"}, true},
		{"audio", Item{Kind: domain.ContentAudio, MimeType: "audio/mpeg"}, true},
		{"static sticker", Item{Kind: domain.ContentSticker, MimeType: "image/webp"}, true},
		{"video sticker", Item{Kind: domain.ContentSticker, MimeType: "video/webm"}, true},
		{"tgs sticker", Item{Kind: domain.ContentSticker, MimeType: "application/x-tgsticker"}, false},
		{"sticker without mime", Item{Kind: domain.ContentSticker}, true},
		{"document", Item{Kind: domain.ContentDocument, MimeType: "application/pdf"}, false},
		{"unknown kind", Item{Kind: "poll"}, false},
		{"image kind wins over a wrong mime", Item{Kind: domain.ContentImage, MimeType: "application/x-tgsticker"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.Source().SupportsAnalysis; got != tt.supports {
				t.Fatalf("SupportsAnalysis = %t, want %t", got, tt.supports)
			}
		})
	}

	it := Item{ID: 4, MessageDBID: 55, Kind: domain.ContentPhoto, FileID: "file-1", FileUniqueID: "unique-1",
		MimeType: "image/jpeg", FileName: "n.jpg", FileSize: 123, Width: 640, Height: 480, Duration: 1.5, Status: domain.MediaPending}
	want := domain.MediaSource{Kind: domain.ContentPhoto, FileID: "file-1", FileUniqueID: "unique-1", MimeType: "image/jpeg",
		FileName: "n.jpg", FileSize: 123, Width: 640, Height: 480, Duration: 1.5, SupportsAnalysis: true}
	if got := it.Source(); got != want {
		t.Fatalf("Source() = %+v, want %+v", got, want)
	}
}

// phase0/02-media.md 1.2: a video sticker is analysed as a video, but its row
// keeps the raw kind.
func TestVideoStickerIsRoutedAsVideo(t *testing.T) {
	it := Item{Kind: domain.ContentSticker, MimeType: "video/webm"}
	if !isVideoKind(domain.AnalysisKind(it.Source())) {
		t.Fatal("a video sticker belongs to the video queue")
	}
	if isVideoKind(domain.AnalysisKind(Item{Kind: domain.ContentSticker, MimeType: "image/webp"}.Source())) {
		t.Fatal("a static sticker is not a video")
	}
}
