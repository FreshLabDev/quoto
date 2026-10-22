// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/i18n"
)

// aiogram's Command filter, as ported in parseCommand: tests/ has no Python
// counterpart, the table is built from aiogram.filters.command.Command.parse_command
// and from the handlers' use of CommandStart (doc 40-bot.md, command parsing).
func TestParseCommand(t *testing.T) {
	tests := []struct {
		name string
		text string
		want command
		ok   bool
	}{
		{"bare", "/start", command{prefix: "/", name: "start"}, true},
		{"payload", "/start quote_5_abc", command{prefix: "/", name: "start", args: "quote_5_abc"}, true},
		{"inner spaces of the arguments stay", "/start  a   b", command{prefix: "/", name: "start", args: "a   b"}, true},
		{"leading whitespace is skipped", "  \n/start x", command{prefix: "/", name: "start", args: "x"}, true},
		{"mention", "/start@Quoto_Test_Bot", command{prefix: "/", name: "start", mention: "Quoto_Test_Bot"}, true},
		{"mention and payload", "/start@bot settings", command{prefix: "/", name: "start", mention: "bot", args: "settings"}, true},
		{"no-break space splits like Python's str.split", "/start x", command{prefix: "/", name: "start", args: "x"}, true},
		{"file separator splits like Python's str.split", "/start\x1fx", command{prefix: "/", name: "start", args: "x"}, true},
		{"any first character is the prefix", "!help me", command{prefix: "!", name: "help", args: "me"}, true},
		{"name is not lower-cased", "/START", command{prefix: "/", name: "START"}, true},
		{"empty", "", command{}, false},
		{"only whitespace", " \t\n", command{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCommand(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseCommand(%q) = %+v, %v; want %+v, %v", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// CommandStart() in the Python handlers: only "/start", case-sensitive, with no
// mention or the bot's own (compared without case), in the text or, for a
// message with a file, its caption.
func TestIsStart(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		m    tg.Message
		want bool
		args string
	}{
		{"start", tg.Message{Text: "/start"}, true, ""},
		{"start with a payload", tg.Message{Text: "/start settings"}, true, "settings"},
		{"own mention", tg.Message{Text: "/start@quoto_test_bot"}, true, ""},
		{"own mention in another case", tg.Message{Text: "/start@Quoto_Test_BOT x"}, true, "x"},
		{"another bot's mention", tg.Message{Text: "/start@otherbot"}, false, ""},
		{"upper case name", tg.Message{Text: "/Start"}, false, ""},
		{"another command", tg.Message{Text: "/startx"}, false, ""},
		{"another prefix", tg.Message{Text: "!start"}, false, ""},
		{"not a command", tg.Message{Text: "start"}, false, ""},
		{"caption of a file", tg.Message{Caption: "/start hi"}, true, "hi"},
		{"the text wins over the caption", tg.Message{Text: "hello", Caption: "/start"}, false, ""},
		{"empty", tg.Message{}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, ok := h.bot.isStart(&tt.m)
			if ok != tt.want || (ok && cmd.args != tt.args) {
				t.Fatalf("isStart(%+v) = %+v, %v; want %v with args %q", tt.m, cmd, ok, tt.want, tt.args)
			}
		})
	}
}

// The collector's own test, _is_command in handlers.py: the text, not the
// caption, begins with a slash.
func TestIsCommandMessage(t *testing.T) {
	tests := []struct {
		m    tg.Message
		want bool
	}{
		{tg.Message{Text: "/start"}, true},
		{tg.Message{Text: "/ok"}, true},
		{tg.Message{Text: "/s/x/y"}, true},
		{tg.Message{Text: "/cmd@otherbot arg"}, true},
		{tg.Message{Text: " /start"}, false},
		{tg.Message{Text: "hello /start"}, false},
		{tg.Message{Caption: "/start"}, false},
		{tg.Message{}, false},
	}
	for _, tt := range tests {
		if got := isCommandMessage(&tt.m); got != tt.want {
			t.Errorf("isCommandMessage(%+v) = %v, want %v", tt.m, got, tt.want)
		}
	}
}

// CMD-01..06 (test_commands.py:36-67): /start is the only command, in the
// default, all_private_chats and all_group_chats scopes; every language and the
// no-language list have both; the descriptions differ by scope; the list without
// a language is English; the default scope is set once.
func TestRegisterCommands(t *testing.T) {
	h := newHarness(t)
	h.bot.registerCommands(context.Background())

	calls := h.api.named("setMyCommands")
	if want := 1 + 2*(1+len(i18n.Codes())); len(calls) != want {
		t.Fatalf("%d setMyCommands calls, want %d", len(calls), want)
	}

	type key struct{ scope, lang string }
	got := map[key]string{}
	defaults := 0
	for _, c := range calls {
		scope := c.Body["scope"].(map[string]any)
		lang, _ := c.Body["language_code"].(string)
		kind := scope["type"].(string)
		if kind == "default" {
			defaults++
		}
		commands := c.Body["commands"].([]any)
		if len(commands) != 1 {
			t.Fatalf("scope %s/%q has %d commands, want only /start", kind, lang, len(commands))
		}
		entry := commands[0].(map[string]any)
		if entry["command"] != "start" {
			t.Errorf("scope %s/%q registers /%v", kind, lang, entry["command"])
		}
		if _, dup := got[key{kind, lang}]; dup {
			t.Errorf("scope %s/%q is set twice", kind, lang)
		}
		got[key{kind, lang}] = entry["description"].(string)
	}
	if defaults != 1 {
		t.Errorf("the default scope is set %d times, want once (CMD-06)", defaults)
	}

	for _, lang := range append([]string{""}, i18n.Codes()...) {
		for _, kind := range []string{"all_private_chats", "all_group_chats"} {
			if _, ok := got[key{kind, lang}]; !ok {
				t.Errorf("CMD-03: no list for %s in language %q", kind, lang)
			}
		}
		want := lang
		if want == "" {
			want = i18n.DefaultLang
		}
		if got[key{"all_private_chats", lang}] != i18n.T(want, "command.start_private") {
			t.Errorf("private description in %q = %q", lang, got[key{"all_private_chats", lang}])
		}
		if got[key{"all_group_chats", lang}] != i18n.T(want, "command.start_group") {
			t.Errorf("group description in %q = %q", lang, got[key{"all_group_chats", lang}])
		}
	}

	// CMD-04, CMD-05: English, and different per scope.
	if got[key{"all_private_chats", ""}] != "Open Quoto" || got[key{"all_group_chats", ""}] != "Open the Quoto panel" {
		t.Errorf("CMD-04/05: the English lists are %q and %q",
			got[key{"all_private_chats", ""}], got[key{"all_group_chats", ""}])
	}
	// The default scope carries the private description, as the older version's did.
	if got[key{"default", ""}] != got[key{"all_private_chats", ""}] {
		t.Errorf("default scope = %q", got[key{"default", ""}])
	}
}

// The commands are published to a bot that may be misconfigured: one refused
// call does not stop the other 34 (doc 40-bot.md, startup).
func TestRegisterCommandsSurvivesAFailingCall(t *testing.T) {
	h := newHarness(t)
	h.api.on("setMyCommands", func(c apiCall, n int) reply {
		if n == 0 || n == 3 {
			return fail(400, "Bad Request: BOT_COMMAND_DESCRIPTION_INVALID")
		}
		return ok("true")
	})
	h.bot.registerCommands(context.Background())
	if got := len(h.api.named("setMyCommands")); got != 35 {
		t.Fatalf("%d calls after one failure, want all 35", got)
	}
}
