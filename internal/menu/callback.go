// SPDX-License-Identifier: Apache-2.0

// Package menu builds Quoto's panels: the text of each screen, its inline
// keyboard and the callback_data behind every button. It does no I/O, so a
// screen can be tested by looking at what it returns.
//
// callback_data is a contract with the panels already sitting in people's
// chats. A button pressed a month after the panel was drawn is parsed by today's
// code, so the grammar and every action name below stay as the Python version
// wrote them (they have been stable since 2026-06-01).
package menu

import (
	"strconv"
	"strings"
)

// CallbackPrefix opens every callback_data this package makes.
const CallbackPrefix = "menu"

// Panel scopes. Anything that is not ScopePrivate is treated as a group by the
// handler, which is how the Python version read a scope it did not know.
const (
	ScopePrivate = "p"
	ScopeGroup   = "g"
)

// Actions, in the order the Python module declared them.
const (
	ActionHome                = "home"
	ActionClose               = "close"
	ActionGroupLanguage       = "lang"
	ActionSetGroupLanguage    = "setlang"
	ActionAutoGroupLanguage   = "autolang"
	ActionGroupSchedule       = "sched"
	ActionGroupTimeAdjust     = "timeadj"
	ActionGroupMinAdjust      = "minadj"
	ActionGroupTimezone       = "tz"
	ActionSetGroupTimezone    = "settz"
	ActionGroupBehavior       = "behavior"
	ActionToggleGroupSetting  = "toggle"
	ActionChatStats           = "chatstats"
	ActionUserStats           = "userstats"
	ActionPrivateLanguage     = "plang"
	ActionSetPrivateLanguage  = "setplang"
	ActionAutoPrivateLanguage = "autoplang"
	ActionAbout               = "about"
	ActionAgreement           = "doc"
)

var knownActions = map[string]bool{
	ActionHome: true, ActionClose: true, ActionGroupLanguage: true, ActionSetGroupLanguage: true,
	ActionAutoGroupLanguage: true, ActionGroupSchedule: true, ActionGroupTimeAdjust: true,
	ActionGroupMinAdjust: true, ActionGroupTimezone: true, ActionSetGroupTimezone: true,
	ActionGroupBehavior: true, ActionToggleGroupSetting: true, ActionChatStats: true,
	ActionUserStats: true, ActionPrivateLanguage: true, ActionSetPrivateLanguage: true,
	ActionAutoPrivateLanguage: true, ActionAbout: true, ActionAgreement: true,
}

// KnownAction reports whether action is one of the nineteen this package
// produces. A name from an older panel is not, and gets an empty answer.
func KnownAction(action string) bool { return knownActions[action] }

// adminOnly are the actions that change the group's settings or open the
// screens for doing so. The check happens on every press, not when the panel is
// drawn, because the person who pressed may have lost their rights since.
var adminOnly = map[string]bool{
	ActionGroupLanguage: true, ActionSetGroupLanguage: true, ActionAutoGroupLanguage: true,
	ActionGroupSchedule: true, ActionGroupTimeAdjust: true, ActionGroupMinAdjust: true,
	ActionGroupTimezone: true, ActionSetGroupTimezone: true, ActionGroupBehavior: true,
	ActionToggleGroupSetting: true,
}

// AdminOnly reports whether pressing action needs a chat administrator.
func AdminOnly(action string) bool { return adminOnly[action] }

// Callback is a parsed callback_data. Owner is the Telegram id of the person
// whose /start drew the panel: nobody else may press its buttons.
type Callback struct {
	Owner   int64
	Scope   string
	Action  string
	Payload string
}

// Data builds callback_data: menu:{owner}:{scope}:{action}[:{payload}]. An empty
// payload is left out, as it was in Python.
func Data(owner int64, scope, action, payload string) string {
	parts := []string{CallbackPrefix, strconv.FormatInt(owner, 10), scope, action}
	if payload != "" {
		parts = append(parts, payload)
	}
	return strings.Join(parts, ":")
}

// Parse reads callback_data. The payload is everything after the fourth colon
// and may itself contain colons. ok is false for another prefix, fewer than four
// parts or a non-numeric owner; the handler answers those with an empty
// acknowledgement.
func Parse(data string) (Callback, bool) {
	if !strings.HasPrefix(data, CallbackPrefix+":") {
		return Callback{}, false
	}
	parts := strings.SplitN(data, ":", 5)
	if len(parts) < 4 {
		return Callback{}, false
	}
	owner, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Callback{}, false
	}
	c := Callback{Owner: owner, Scope: parts[2], Action: parts[3]}
	if len(parts) == 5 {
		c.Payload = parts[4]
	}
	return c, true
}
