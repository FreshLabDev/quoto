// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/agreement"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

func hasData(data []string, prefix string) bool {
	for _, d := range data {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

// HND-12 (test_handlers.py:403): without rich Markdown the tab is HTML with the
// expandable blockquote, the Accept button for an admin of a group that has not
// accepted, and Back and Close last.
func TestAgreementTabInAGroupAsHTML(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("ru"), panelMessage(), "menu:777:g:doc")

	e := h.lastEdit()
	if !strings.Contains(e.text(), "<blockquote expandable>") || strings.Contains(e.text(), "\n# ") {
		t.Fatalf("not the HTML rendering:\n%.300s", e.text())
	}
	if e.str("parse_mode") != "HTML" {
		t.Fatalf("parse mode %q", e.str("parse_mode"))
	}
	if !hasData(e.data(), "agree:a:ru:g:777") {
		t.Fatalf("no Accept button: %v", e.data())
	}
	labels := e.labels()
	if tail := labels[len(labels)-2:]; tail[0] != "Назад" || tail[1] != "Закрыть" {
		t.Fatalf("last buttons %v", tail)
	}
	if n := len(h.api.named("editMessageText")); n != 1 {
		t.Fatalf("%d edits", n)
	}
	h.requireEmptyAnswer()
}

// HND-11 (test_handlers.py:372): on a server with rich Markdown the tab is sent
// as a rich message: Markdown starting with a heading, the contact in it, no
// HTML text next to it.
func TestAgreementTabAsRichMarkdown(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.bot.rich.set(richOn)
	h.press(owner("ru"), panelMessage(), "menu:777:g:doc")

	edits := h.api.named("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("%d edits", len(edits))
	}
	body := edits[0].Body
	rich, _ := body["rich_message"].(map[string]any)
	md, _ := rich["markdown"].(string)
	if !strings.HasPrefix(md, "# ") || !strings.Contains(md, "@amtiyo") {
		t.Fatalf("markdown %.200q", md)
	}
	if _, has := body["text"]; has {
		t.Fatalf("the rich edit carries text: %v", body)
	}
	if _, has := body["parse_mode"]; has {
		t.Fatalf("the rich edit carries a parse mode: %v", body)
	}
	if !hasData(edit{edits[0]}.data(), "agree:a:ru:g:777") {
		t.Fatalf("keyboard %v", edit{edits[0]}.data())
	}
}

// The three states the tab can be opened in: an admin of a group that has not
// accepted (can accept), a member (reads only), and anyone in a group that has
// accepted (reads only).
func TestAgreementTabByViewer(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		accepted bool
		button   bool
	}{
		{"admin, not accepted", "administrator", false, true},
		{"creator, not accepted", "creator", false, true},
		{"member, not accepted", "member", false, false},
		{"admin, accepted", "administrator", true, false},
		{"member, accepted", "member", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.api.setMember(ownerID, tt.status)
			if tt.accepted {
				at, version := h.clock.Now(), domain.DocumentVersion
				g := h.store.group(groupID)
				g.AgreementAcceptedAt, g.AgreementVersion = &at, &version
			}
			h.press(owner("ru"), panelMessage(), "menu:777:g:doc")
			e := h.lastEdit()
			if got := hasData(e.data(), "agree:a:"); got != tt.button {
				t.Fatalf("Accept button = %v, want %v", got, tt.button)
			}
			want := agreement.Build("ru", agreement.Options{
				CanAccept: tt.button, Accepted: tt.accepted,
				Ref: agreement.Ref{Scope: "g", Owner: ownerID},
				Nav: menu.NavRow(ownerID, "g", "ru"),
			})
			if e.text() != want.HTML {
				t.Fatalf("document differs from the one built for this viewer")
			}
		})
	}
}

// HND-13 (test_handlers.py:431) for the agreement: in a private chat the tab has
// Back only, never Accept, and asks Telegram nothing about rights.
func TestAgreementTabInPrivate(t *testing.T) {
	h := newHarness(t)
	h.press(owner("ru"), privatePanel(), "menu:777:p:doc")
	e := h.lastEdit()
	if hasData(e.data(), "agree:a:") {
		t.Fatal("Accept in a private chat")
	}
	labels := e.labels()
	if labels[len(labels)-1] != "Назад" || (len(labels) > 1 && labels[len(labels)-2] == "Закрыть") {
		t.Fatalf("last buttons %v", labels[len(labels)-2:])
	}
	if !hasData(e.data(), "agree:v:de:p:777") {
		t.Fatalf("the language switch lost the panel: %v", e.data())
	}
	if len(h.api.named("getChatMember")) != 0 || len(h.store.ensures) != 0 {
		t.Fatal("a private tab looked at a group")
	}
}

// HND-14 (test_handlers.py:452): switching the document's language keeps the
// panel: German heading, and Back leads to the same panel's home.
func TestAgreementLanguageSwitchKeepsThePanel(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("ru"), panelMessage(), "agree:v:de:g:777")
	e := h.lastEdit()
	want := agreement.Build("de", agreement.Options{
		CanAccept: true, Ref: agreement.Ref{Scope: "g", Owner: ownerID}, Nav: menu.NavRow(ownerID, "g", "de"),
	})
	if e.text() != want.HTML {
		t.Fatalf("not the German document:\n%.300s", e.text())
	}
	if !hasData(e.data(), "menu:777:g:home") {
		t.Fatalf("Back does not lead home: %v", e.data())
	}
	h.requireEmptyAnswer()
}

// HND-15 (test_handlers.py:480): a panel tab pressed by somebody else is a
// dialog in the language on the button, with no edit.
func TestAgreementTabOfAnotherPerson(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(person(888, "ru"), panelMessage(), "agree:v:de:g:777")
	text, alert := h.oneAnswer()
	if !alert || text != i18n.T("de", "menu.other_user") {
		t.Fatalf("answer %q alert=%v", text, alert)
	}
	h.requireNoEdit()
	if len(h.store.ensures) != 0 {
		t.Fatal("the group was read")
	}
}

// The welcome message belongs to nobody: anybody may read it, in any language,
// with no panel to go back to; an admin gets Accept.
func TestAgreementFromTheWelcomeMessage(t *testing.T) {
	t.Run("a member reads", func(t *testing.T) {
		h := newHarness(t)
		h.press(person(555, "en"), panelMessage(), "agree:v:fr")
		e := h.lastEdit()
		want := agreement.Build("fr", agreement.Options{})
		if e.text() != want.HTML {
			t.Fatalf("document differs:\n%.200s", e.text())
		}
		if hasData(e.data(), "menu:") || hasData(e.data(), "agree:a:") {
			t.Fatalf("keyboard %v", e.data())
		}
		if !hasData(e.data(), "agree:v:de") {
			t.Fatalf("the language switch is missing: %v", e.data())
		}
	})
	t.Run("an admin can accept", func(t *testing.T) {
		h := newHarness(t)
		h.api.setMember(555, "administrator")
		h.press(person(555, "en"), panelMessage(), "agree:v:ru")
		if !hasData(h.lastEdit().data(), "agree:a:ru") {
			t.Fatalf("keyboard %v", h.lastEdit().data())
		}
	})
	t.Run("an unknown language is English", func(t *testing.T) {
		h := newHarness(t)
		h.press(person(555, "en"), panelMessage(), "agree:v:xx")
		if h.lastEdit().text() != agreement.Build("en", agreement.Options{}).HTML {
			t.Fatal("not the English document")
		}
	})
	t.Run("the rights lookup fails", func(t *testing.T) {
		h := newHarness(t)
		h.api.memberFails = true
		h.press(person(555, "en"), panelMessage(), "agree:v:ru")
		text, alert := h.oneAnswer()
		if !alert || text != i18n.T("ru", "settings.invalid") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
		h.requireNoEdit()
	})
}

// Accepting (handlers.py agreement_callback): an admin's press records the
// acceptance in the language of the button, replaces the document with the
// confirmation and Back/Close, toasts, and asks the scheduler to catch up.
func TestAgreementAccept(t *testing.T) {
	t.Run("from a panel tab", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.catchup.called = make(chan int64, 1)
		h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")

		if got := h.store.accepted; len(got) != 1 || got[0] != "-1001234567890 777 ru" {
			t.Fatalf("accepted %v", got)
		}
		e := h.lastEdit()
		if e.text() != agreement.AcceptedText("ru") || e.str("parse_mode") != "HTML" {
			t.Fatalf("confirmation %q", e.text())
		}
		if want := []string{"Назад", "Закрыть"}; strings.Join(e.labels(), "|") != strings.Join(want, "|") {
			t.Fatalf("buttons %v", e.labels())
		}
		text, alert := h.oneAnswer()
		if alert || text != i18n.T("ru", "agreement.accepted_toast") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
		if got := <-h.catchup.called; got != groupID {
			t.Fatalf("catch-up for %d", got)
		}
		h.bot.wg.Wait()
	})

	t.Run("from the welcome message the buttons go away", func(t *testing.T) {
		h := newHarness(t)
		h.api.setMember(ownerID, "administrator")
		h.press(owner("ru"), panelMessage(), "agree:a:ru")
		body := h.lastEdit().Body
		markup, ok := body["reply_markup"].(map[string]any)
		if !ok {
			t.Fatalf("no reply_markup in %v: an edit without one would leave the buttons", body)
		}
		if rows, _ := markup["inline_keyboard"].([]any); rows == nil || len(rows) != 0 {
			t.Fatalf("keyboard %v, want an explicitly empty one", markup)
		}
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), `"inline_keyboard":[]`) {
			t.Fatalf("body %s", raw)
		}
		h.bot.wg.Wait()
	})

	t.Run("a member may not", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.api.setMember(ownerID, "member")
		h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
		text, alert := h.oneAnswer()
		if !alert || text != i18n.T("ru", "agreement.admin_only") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
		if len(h.store.accepted) != 0 {
			t.Fatal("recorded")
		}
		h.requireNoEdit()
		if len(h.catchup.calls()) != 0 {
			t.Fatal("caught up")
		}
	})

	t.Run("not in a group", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("ru"), privatePanel(), "agree:a:ru:p:777")
		h.requireEmptyAnswer()
		if len(h.store.accepted) != 0 {
			t.Fatal("recorded")
		}
	})

	t.Run("the rights lookup fails", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.api.memberFails = true
		h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
		text, alert := h.oneAnswer()
		if !alert || text != i18n.T("ru", "settings.invalid") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
		if len(h.store.accepted) != 0 {
			t.Fatal("recorded")
		}
	})

	t.Run("somebody else's panel", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.api.setMember(888, "administrator")
		h.press(person(888, "ru"), panelMessage(), "agree:a:ru:g:777")
		text, alert := h.oneAnswer()
		if !alert || text != i18n.T("ru", "menu.other_user") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
		if len(h.store.accepted) != 0 {
			t.Fatal("recorded")
		}
	})

	t.Run("a store failure is an error and the press is answered", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.store.errs["AcceptAgreement"] = errBoom()
		h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
		if len(h.answers()) != 1 || len(h.adapter.alertKeys()) != 1 {
			t.Fatalf("answers %d alerts %v", len(h.answers()), h.adapter.alertKeys())
		}
		h.requireNoEdit()
		if len(h.catchup.calls()) != 0 {
			t.Fatal("caught up after a failure")
		}
	})

	t.Run("an edit that fails still toasts and catches up", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.api.answerWith("editMessageText", fail(400, "Bad Request: message to edit not found"))
		h.catchup.called = make(chan int64, 1)
		h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
		text, _ := h.oneAnswer()
		if text != i18n.T("ru", "agreement.accepted_toast") {
			t.Fatalf("toast %q", text)
		}
		<-h.catchup.called
		h.bot.wg.Wait()
	})

	t.Run("no scheduler wired", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.bot.d.CatchUp = nil
		h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
		if len(h.store.accepted) != 1 || len(h.adapter.alertKeys()) != 0 {
			t.Fatal("a missing scheduler broke the acceptance")
		}
	})
}

// HND-25 (test_handlers.py:793): after the acceptance the group's closed window
// is processed, and what goes wrong in there is swallowed: logged and sent to the
// developers, never thrown at the press that already succeeded.
func TestAgreementCatchUpFailureIsContained(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.catchup.panics = true
	h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
	h.bot.wg.Wait()

	if len(h.store.accepted) != 1 || len(h.answers()) != 1 {
		t.Fatalf("accepted %v, answers %d", h.store.accepted, len(h.answers()))
	}
	if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:catch_up" {
		t.Fatalf("alerts %v", got)
	}
}

// The catch-up runs apart from the press and is waited for on shutdown: Run does
// not return while it is going (checked here through the wait group).
func TestAgreementCatchUpUsesTheRunContext(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	ctx, cancel := context.WithCancel(context.Background())
	h.bot.runMu.Lock()
	h.bot.runCtx = ctx
	h.bot.runMu.Unlock()
	got := make(chan context.Context, 1)
	h.bot.d.CatchUp = catchUpFunc(func(ctx context.Context, chatID int64) { got <- ctx })
	h.press(owner("ru"), panelMessage(), "agree:a:ru:g:777")
	h.bot.wg.Wait()
	cancel()
	if c := <-got; c.Err() == nil {
		t.Fatal("the catch-up did not get the run context")
	}
}

type catchUpFunc func(ctx context.Context, chatID int64)

func (f catchUpFunc) CatchUp(ctx context.Context, chatID int64) { f(ctx, chatID) }

// The welcome keyboard's button is the one the group sees under the greeting.
func TestWelcomeButtonOpensTheDocument(t *testing.T) {
	data := dataOf(agreement.WelcomeKeyboard("de"))
	if len(data) != 1 || data[0] != "agree:v:de" {
		t.Fatalf("AGR-12: %v", data)
	}
}
