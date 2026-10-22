// SPDX-License-Identifier: Apache-2.0

// Package agreement renders the user agreement and the buttons around it. The
// document is authored once, as structure (a title, numbered sections, one of
// them a list, an inline caution, a signature) and rendered twice: as rich
// Markdown for a Bot API 10.3 server, where Telegram draws real headings, lists
// and blockquotes, and as Telegram HTML everywhere else, which can only imitate
// that with bold text inside an expandable blockquote.
//
// Group admins accept it with inline buttons; the same view is a read-only tab
// of the /start panel. The text lives in the locale files and is not edited
// here.
package agreement

import (
	"strconv"
	"strings"
)

// CallbackPrefix opens every callback_data this package makes.
const CallbackPrefix = "agree"

// Actions: view the document (or switch its language), and accept it.
const (
	ActionView   = "v"
	ActionAccept = "a"
)

// Ref names the panel an agreement tab was opened from, so that switching the
// document's language keeps it inside that panel and its Back button still
// works. The zero Ref means the tab was opened from the welcome message or the
// reminder, which belong to no one.
type Ref struct {
	Scope string
	Owner int64
}

// Callback is a parsed callback_data. HasOwner is true for the five-part form,
// where Owner is the person the tab belongs to.
type Callback struct {
	Action   string
	Lang     string
	Scope    string
	Owner    int64
	HasOwner bool
}

// Ref is the panel the tab came from; ok is false unless both parts are there.
func (c Callback) Ref() (Ref, bool) {
	if c.Scope == "" || !c.HasOwner {
		return Ref{}, false
	}
	return Ref{Scope: c.Scope, Owner: c.Owner}, true
}

// Data builds callback_data: agree:{action}:{lang}[:{scope}:{owner}]. The panel
// part is written only when ref names one.
func Data(action, lang string, ref Ref) string {
	data := CallbackPrefix + ":" + action + ":" + lang
	if ref.Scope != "" {
		data += ":" + ref.Scope + ":" + strconv.FormatInt(ref.Owner, 10)
	}
	return data
}

// Parse reads callback_data. Exactly three or five parts are valid ("agree:v:en:g"
// is not), and in the five-part form the owner must be a number.
func Parse(data string) (Callback, bool) {
	if !strings.HasPrefix(data, CallbackPrefix+":") {
		return Callback{}, false
	}
	parts := strings.SplitN(data, ":", 5)
	switch len(parts) {
	case 3:
		return Callback{Action: parts[1], Lang: parts[2]}, true
	case 5:
		owner, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			return Callback{}, false
		}
		return Callback{Action: parts[1], Lang: parts[2], Scope: parts[3], Owner: owner, HasOwner: true}, true
	}
	return Callback{}, false
}
