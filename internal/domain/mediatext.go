// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"strconv"
	"strings"
)

// MediaPromptVersion is the version of the media description prompt. Cache
// entries are keyed by it: a description written by another prompt is not
// reused. Change it together with the prompt text (internal/ai tests pin the
// pair). v3 starts the Go version; v1 and v2 rows stay in the table unread.
const MediaPromptVersion = "v3"

// The placeholders below are stored in messages.text and reach the model, so
// they stay as the Python version wrote them, in Russian.
const (
	emptyMediaSuffix = "сообщение без текста"
	metadataPrefix   = "Файл: "
)

// CanonicalText is the text a media message is stored and scored with: the
// caption, then "<kind>: <description>", one per line. With neither it is the
// "no text" placeholder.
func CanonicalText(kind, caption, description string) string {
	var parts []string
	if caption != "" {
		parts = append(parts, PyStrip(caption))
	}
	if description != "" {
		parts = append(parts, kind+": "+PyStrip(description))
	}
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return EmptyMediaText(kind)
	}
	return strings.Join(kept, "\n")
}

// EmptyMediaText is the stored text of a message with nothing to say:
// "<kind>: сообщение без текста".
func EmptyMediaText(kind string) string { return kind + ": " + emptyMediaSuffix }

// MetadataDescription describes a file by its metadata alone. It is the
// description of media that was not analysed (switched off, unsupported, failed,
// or from a group without the accepted agreement).
func MetadataDescription(src MediaSource) string {
	details := []string{src.Kind}
	if src.FileName != "" {
		details = append(details, "name="+src.FileName)
	}
	if src.MimeType != "" {
		details = append(details, "mime="+src.MimeType)
	}
	if src.FileSize != 0 {
		details = append(details, "size="+strconv.FormatInt(src.FileSize, 10))
	}
	if src.Duration != 0 {
		// Python's :g, six significant digits: 10s, 10.5s, 1e+06s.
		details = append(details, "duration="+strconv.FormatFloat(src.Duration, 'g', 6, 64)+"s")
	}
	return metadataPrefix + strings.Join(details, ", ")
}

// AnalysisKind is the kind the cache, the normaliser and the prompt work with.
// It differs from the stored kind for a video sticker, which is analysed as a
// video; message_media.media_kind and messages.content_type keep the raw kind.
func AnalysisKind(src MediaSource) string {
	if src.Kind == ContentSticker && src.MimeType == "video/webm" {
		return ContentVideo
	}
	return src.Kind
}
