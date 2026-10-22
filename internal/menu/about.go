// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"strings"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// scoring names the providers on the About card (F13): Claude scores the day
// and OpenRouter takes over when Claude cannot. Proper names, so every language
// shows the same.
const scoring = "Claude, OpenRouter"

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
			i18n.T(lang, "about.eval") + " · " + scoring,
			i18n.T(lang, "about.sources") + " · " +
				`<a href="` + buildinfo.Repository + `">` + repoName + `</a> · ` + buildinfo.License,
			i18n.T(lang, "about.admin") + ` · <a href="` + buildinfo.ContactURL + `">` + buildinfo.Contact + `</a>`,
		},
		Version: "v" + strings.TrimPrefix(version, "v"),
	}.HTML()
}
