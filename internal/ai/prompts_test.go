// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// doc section 6 "Prompts": byte for byte, from the texts app/ai.py produced
// (testdata/*.txt, extracted from the Python source with ast).
func TestPromptsMatchPython(t *testing.T) {
	cases := []struct {
		file string
		got  string
	}{
		{"day_prompt.txt", dayPrompt},
		{"injection_guard.txt", injectionGuard},
		{"language_detection_prompt.txt", languageDetectionPrompt},
		{"image_description_prompt.txt", imageDescriptionPrompt},
		{"sticker_description_prompt.txt", stickerDescriptionPrompt},
		{"video_description_prompt.txt", videoDescriptionPrompt},
		{"audio_description_prompt.txt", audioDescriptionPrompt},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			if want := golden(t, tc.file); tc.got != want {
				t.Errorf("prompt differs from the Python text\n got: %q\nwant: %q", tc.got, want)
			}
		})
	}
}

// doc section 6 "Prompts": the two spaces and the missing one.
func TestPromptQuirksArePreserved(t *testing.T) {
	if !strings.Contains(dayPrompt, `<0-10>}]}.Keep reason_text under 160 characters.`) {
		t.Error("the space after `}]}.` was added: the Python prompt does not have it")
	}
	for name, p := range map[string]string{"injection guard": injectionGuard, "language detection": languageDetectionPrompt} {
		if !strings.HasPrefix(p, " ") {
			t.Errorf("%s prompt must start with a space", name)
		}
	}
	if dayPrompt[0] == ' ' {
		t.Error("the day prompt does not start with a space")
	}
}

// tests/test_ai.py::test_injection_guard_appended_to_system_prompt (AI-23), and
// AI-01: the system prompt carries what the model has to be told.
func TestPromptContents(t *testing.T) {
	for _, want := range []string{"use ONLY `i`", "quote curator", "should_publish", "context_ids", "never choose a sticker as `primary_id`"} {
		if !strings.Contains(dayPrompt, want) {
			t.Errorf("day prompt lacks %q", want)
		}
	}
	if !strings.Contains(injectionGuard, "untrusted data") {
		t.Error("injection guard lacks 'untrusted data'")
	}
}

// doc section 6: the language names in the detection prompt come from the locale
// files, in the order the Python prompt listed them.
func TestLanguagePromptListsTheLocales(t *testing.T) {
	list := i18n.PromptList()
	if !strings.Contains(languageDetectionPrompt, "options: "+list+". Prefer") {
		t.Errorf("language list missing or misplaced")
	}
	if !strings.Contains(languageDetectionPrompt, `"interface_language": "ru|uk|en|de|es|fr|it|pl|cs|tr|sv|be|ca|zh|ja|ar"}.`) {
		t.Error("code list missing")
	}
	if !strings.HasPrefix(list, "ru=Русский, uk=Українська, en=English, de=Deutsch,") {
		t.Errorf("list = %q", list)
	}
}

// 10-ai: the media prompt text and its version move together. The cache is
// keyed by the version, so editing a prompt without bumping it keeps serving
// descriptions the new prompt never wrote.
func TestMediaPromptsArePinnedToTheirVersion(t *testing.T) {
	pinned := map[string]string{
		"v3": "017053a28e1a9153d2d68a70483e54719a7ad104ca1962a010445a567a638d86",
	}
	want, ok := pinned[domain.MediaPromptVersion]
	if !ok {
		t.Fatalf("domain.MediaPromptVersion is %q, which has no digest here: add %q: %q to pinned",
			domain.MediaPromptVersion, domain.MediaPromptVersion, mediaPromptDigest())
	}
	if got := mediaPromptDigest(); got != want {
		t.Fatalf("the media prompts changed but domain.MediaPromptVersion is still %q.\n"+
			"Bump the version (internal/domain/mediatext.go) and pin the new digest %q under the new version here.",
			domain.MediaPromptVersion, got)
	}
}

func TestMediaPromptSelection(t *testing.T) {
	cases := map[string]string{
		"voice": audioDescriptionPrompt, "audio": audioDescriptionPrompt,
		"video": videoDescriptionPrompt, "animation": videoDescriptionPrompt, "video_note": videoDescriptionPrompt,
		"sticker": stickerDescriptionPrompt,
		"photo":   imageDescriptionPrompt, "image": imageDescriptionPrompt, "": imageDescriptionPrompt, "document": imageDescriptionPrompt,
	}
	for kind, want := range cases {
		if got := mediaPrompt(kind); got != want {
			t.Errorf("mediaPrompt(%q) is the wrong prompt", kind)
		}
	}
}
