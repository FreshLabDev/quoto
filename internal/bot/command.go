// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"strings"
	"unicode"

	"github.com/FreshLabDev/tg"
)

// command is a parsed "/name@mention args".
type command struct {
	prefix  string
	name    string
	mention string
	args    string
}

// isSpace is Python's str.isspace, which splits the text of a command.
func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// parseCommand reads a command the way aiogram's Command filter did, because
// which messages count as /start is part of the behaviour the groups have seen:
// the text is split once on whitespace, the first character is the prefix, the
// name runs to an "@" and the mention follows it. Nothing is lower-cased.
func parseCommand(text string) (command, bool) {
	text = strings.TrimLeftFunc(text, isSpace)
	if text == "" {
		return command{}, false
	}
	full, rest := text, ""
	if i := strings.IndexFunc(text, isSpace); i >= 0 {
		full, rest = text[:i], strings.TrimLeftFunc(text[i:], isSpace)
	}
	runes := []rune(full)
	name, mention, _ := strings.Cut(string(runes[1:]), "@")
	return command{prefix: string(runes[0]), name: name, mention: mention, args: rest}, true
}

// isStart reports whether m is "/start", or "/start@thisbot", with or without
// arguments. The filter reads the caption when there is no text.
func (b *Bot) isStart(m *tg.Message) (command, bool) {
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	cmd, ok := parseCommand(text)
	if !ok || cmd.prefix != "/" || cmd.name != "start" {
		return command{}, false
	}
	if cmd.mention != "" && strings.ToLower(cmd.mention) != b.username {
		return command{}, false
	}
	return cmd, true
}

// isCommandMessage is the collector's test: the text, not the caption, begins
// with a slash. "/ok" and "/s/x/y" count, so do commands for other bots.
func isCommandMessage(m *tg.Message) bool {
	return strings.HasPrefix(m.Text, "/")
}
