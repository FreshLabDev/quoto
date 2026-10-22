// SPDX-License-Identifier: Apache-2.0

package domain

import "testing"

func TestCanonicalText(t *testing.T) {
	// app/media.py::canonical_text
	for name, tc := range map[string]struct{ kind, caption, desc, want string }{
		"caption and description": {"photo", " hi ", " a cat ", "hi\nphoto: a cat"},
		"description only":        {"video", "", "a dog", "video: a dog"},
		"caption only":            {"photo", "just words", "", "just words"},
		"neither":                 {"voice", "", "", "voice: сообщение без текста"},
		"blank caption":           {"photo", "   ", "", "photo: сообщение без текста"},
		"blank description":       {"photo", "", "  ", "photo: "},
	} {
		if got := CanonicalText(tc.kind, tc.caption, tc.desc); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestMetadataDescription(t *testing.T) {
	// app/media.py::_metadata_description; duration is Python's :g.
	for name, tc := range map[string]struct {
		src  MediaSource
		want string
	}{
		"kind only": {MediaSource{Kind: "photo"}, "Файл: photo"},
		"everything": {MediaSource{Kind: "video", FileName: "a.mp4", MimeType: "video/mp4", FileSize: 1234, Duration: 10},
			"Файл: video, name=a.mp4, mime=video/mp4, size=1234, duration=10s"},
		"fraction":      {MediaSource{Kind: "audio", Duration: 10.5}, "Файл: audio, duration=10.5s"},
		"large":         {MediaSource{Kind: "audio", Duration: 1e6}, "Файл: audio, duration=1e+06s"},
		"six digits":    {MediaSource{Kind: "audio", Duration: 100000}, "Файл: audio, duration=100000s"},
		"rounded":       {MediaSource{Kind: "audio", Duration: 1234567}, "Файл: audio, duration=1.23457e+06s"},
		"zero is empty": {MediaSource{Kind: "audio", Duration: 0, FileSize: 0}, "Файл: audio"},
	} {
		if got := MetadataDescription(tc.src); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestAnalysisKind(t *testing.T) {
	for name, tc := range map[string]struct {
		src  MediaSource
		want string
	}{
		"image":         {MediaSource{Kind: "image"}, "image"},
		"video sticker": {MediaSource{Kind: "sticker", MimeType: "video/webm"}, "video"},
		"webp sticker":  {MediaSource{Kind: "sticker", MimeType: "image/webp"}, "sticker"},
		"photo":         {MediaSource{Kind: "photo"}, "photo"},
	} {
		if got := AnalysisKind(tc.src); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
