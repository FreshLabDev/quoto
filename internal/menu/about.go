// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"strings"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// evalKey is the locale entry for the scoring line of the About card, "Claude,
// OpenRouter as fallback" in each language (F13). It is not in translations.json
// yet; until it is, the card names the two providers without the word for
// "fallback", which is true and needs no translation.
const evalKey = "about.eval_value"

const evalFallback = "Claude, OpenRouter"

func evalValue(lang string) string {
	if s, ok := i18n.Value(lang, evalKey).(string); ok && s != "" {
		return s
	}
	return evalFallback
}

// AboutText is the family About card: name, version, one line of purpose, then
// the facts as "key · value" rows. The repository is a link in the text, so no
// button repeats it. The licence and the repository come from buildinfo, never
// from a literal here.
func AboutText(lang, version string) string {
	repoName := strings.TrimPrefix(buildinfo.Repository, "https://github.com/")
	return Screen{
		Title: buildinfo.Name,
		Hint:  i18n.T(lang, "about.tagline"),
		Lines: []string{
			i18n.T(lang, "about.eval") + " · " + evalValue(lang),
			i18n.T(lang, "about.sources") + " · " +
				`<a href="` + buildinfo.Repository + `">` + repoName + `</a> · ` + buildinfo.License,
			i18n.T(lang, "about.admin") + ` · <a href="` + buildinfo.ContactURL + `">` + buildinfo.Contact + `</a>`,
		},
		Version: "v" + strings.TrimPrefix(version, "v"),
	}.HTML()
}
