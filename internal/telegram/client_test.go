// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/i18n"
)

const testToken = "123456:SECRET-TOKEN-VALUE"

// call is one request the fake Bot API saw.
type call struct {
	Method string
	Body   map[string]any
	Query  url.Values
}

// reply is what the fake answers: an HTTP status and a body. A zero reply is a
// plain {"ok":true,"result":true}.
type reply struct {
	status int
	body   string
}

func okResult(result string) reply { return reply{200, `{"ok":true,"result":` + result + `}`} }

func failure(code int, description string) reply {
	return reply{code, fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, code, description)}
}

// flood is a 429 without retry_after. tg replays a POST on its own only when a
// retry_after is present, so this exercises the adapter's loop without tg
// sleeping for real.
func flood() reply { return failure(429, "Too Many Requests: retry later") }

type fakeAPI struct {
	mu    sync.Mutex
	calls []call
	// script answers by method, in order; the last entry repeats.
	script map[string][]reply
}

func newAPI(t *testing.T, script map[string][]reply) (*tg.Client, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{script: script}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		c := call{Method: method, Query: r.URL.Query()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.Body)
		}
		api.mu.Lock()
		api.calls = append(api.calls, c)
		seen := 0
		for _, prev := range api.calls[:len(api.calls)-1] {
			if prev.Method == method {
				seen++
			}
		}
		answers := api.script[method]
		rep := reply{200, `{"ok":true,"result":true}`}
		if len(answers) > 0 {
			rep = answers[min(seen, len(answers)-1)]
		}
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return tg.New(testToken, tg.WithAPIBase(srv.URL)), api
}

func (a *fakeAPI) named(method string) []call {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []call
	for _, c := range a.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// clock is a fake time source that records the waits asked of it.
type clock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	return nil
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newAdapter(t *testing.T, script map[string][]reply, cfg Config) (*Client, *fakeAPI, *clock) {
	t.Helper()
	client, api := newAPI(t, script)
	clk := &clock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	cfg.Now, cfg.Sleep = clk.Now, clk.Sleep
	return New(client, cfg, nil), api, clk
}

// doc §6 / brief 4: silent HTML without link previews, every time.
func TestSendHTMLIsSilentWithoutPreview(t *testing.T) {
	a, api, _ := newAdapter(t, map[string][]reply{"sendMessage": {okResult(`{"message_id":77,"chat":{"id":-100,"type":"supergroup"},"date":1}`)}}, Config{})
	id, err := a.SendHTML(context.Background(), -100, "<b>hi</b>")
	if err != nil || id != 77 {
		t.Fatalf("SendHTML = %d, %v", id, err)
	}
	calls := api.named("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	body := calls[0].Body
	if body["chat_id"] != float64(-100) || body["text"] != "<b>hi</b>" || body["parse_mode"] != "HTML" || body["disable_notification"] != true {
		t.Errorf("body %v", body)
	}
	preview, _ := body["link_preview_options"].(map[string]any)
	if preview["is_disabled"] != true {
		t.Errorf("link_preview_options %v", body["link_preview_options"])
	}
	for _, absent := range []string{"reply_markup", "message_thread_id"} {
		if _, has := body[absent]; has {
			t.Errorf("%s was sent", absent)
		}
	}
}

// doc §6 / brief 6: the thread id is sent only when the caller sets it.
func TestSendCarriesThreadAndMarkup(t *testing.T) {
	a, api, _ := newAdapter(t, map[string][]reply{"sendMessage": {okResult(`{"message_id":5,"chat":{"id":1,"type":"supergroup"},"date":1}`)}}, Config{})
	markup := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{{Text: "A", CallbackData: "a"}}}}
	msg, err := a.Send(context.Background(), SendParams{ChatID: 1, ThreadID: 42, Text: "t", Markup: markup})
	if err != nil || msg.MessageID != 5 {
		t.Fatalf("Send = %+v, %v", msg, err)
	}
	body := api.named("sendMessage")[0].Body
	if body["message_thread_id"] != float64(42) {
		t.Errorf("thread %v", body["message_thread_id"])
	}
	kb, _ := body["reply_markup"].(map[string]any)
	if rows, _ := kb["inline_keyboard"].([]any); len(rows) != 1 {
		t.Errorf("reply_markup %v", body["reply_markup"])
	}
	// A handler's answer is tried once: a 429 comes back to the caller.
	a2, api2, clk := newAdapter(t, map[string][]reply{"sendMessage": {flood()}}, Config{})
	if _, err := a2.Send(context.Background(), SendParams{ChatID: 1, Text: "t"}); !tg.IsTooManyRequests(err) {
		t.Errorf("Send under flood = %v", err)
	}
	if n := len(api2.named("sendMessage")); n != 1 || len(clk.slept) != 0 {
		t.Errorf("Send retried: %d calls, waits %v", n, clk.slept)
	}
}

// tests/test_scheduler.py::_send_with_retry (min(retry_after, 60) + 1 between three attempts)
func TestFloodRetryLoop(t *testing.T) {
	msg := okResult(`{"message_id":9,"chat":{"id":1,"type":"supergroup"},"date":1}`)
	cases := []struct {
		name      string
		script    []reply
		wantErr   bool
		wantCalls int
		wantWaits []time.Duration
	}{
		{"first try", []reply{msg}, false, 1, nil},
		{"one flood wait", []reply{flood(), msg}, false, 2, []time.Duration{time.Second}},
		{"two flood waits", []reply{flood(), flood(), msg}, false, 3, []time.Duration{time.Second, time.Second}},
		{"gives up after three attempts", []reply{flood()}, true, 3, []time.Duration{time.Second, time.Second}},
		{"bad request is not retried", []reply{failure(400, "Bad Request: chat not found")}, true, 1, nil},
		{"server error is not retried", []reply{failure(502, "Bad Gateway")}, true, 1, nil},
		{"flood then refusal", []reply{flood(), failure(403, "Forbidden: bot was kicked")}, true, 2, []time.Duration{time.Second}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, api, clk := newAdapter(t, map[string][]reply{"sendMessage": c.script}, Config{})
			id, err := a.SendHTML(context.Background(), 1, "x")
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !c.wantErr && id != 9 {
				t.Errorf("id = %d", id)
			}
			if n := len(api.named("sendMessage")); n != c.wantCalls {
				t.Errorf("%d calls, want %d", n, c.wantCalls)
			}
			if fmt.Sprint(clk.slept) != fmt.Sprint(c.wantWaits) {
				t.Errorf("waits %v, want %v", clk.slept, c.wantWaits)
			}
		})
	}
}

// tests/test_scheduler.py::_send_with_retry
func TestFloodDelay(t *testing.T) {
	retryAfter := func(seconds int) error {
		return &tg.APIError{Method: "sendMessage", StatusCode: 429, ErrorCode: 429, RetryAfter: time.Duration(seconds) * time.Second}
	}
	cases := []struct {
		name  string
		err   error
		want  time.Duration
		flood bool
	}{
		{"five seconds", retryAfter(5), 6 * time.Second, true},
		{"none given", retryAfter(0), time.Second, true},
		{"exactly the cap", retryAfter(60), 61 * time.Second, true},
		{"above the cap", retryAfter(3600), 61 * time.Second, true},
		{"joined with the context error tg adds when its own wait was cut", errors.Join(retryAfter(30), context.DeadlineExceeded), 31 * time.Second, true},
		{"wrapped", fmt.Errorf("send: %w", retryAfter(2)), 3 * time.Second, true},
		{"bad request", &tg.APIError{StatusCode: 400, ErrorCode: 400}, 0, false},
		{"server error", &tg.APIError{StatusCode: 500, ErrorCode: 500}, 0, false},
		{"not an api error", errors.New("boom"), 0, false},
		{"nil", nil, 0, false},
	}
	for _, c := range cases {
		got, flood := floodDelay(c.err)
		if got != c.want || flood != c.flood {
			t.Errorf("%s: floodDelay = %v, %v; want %v, %v", c.name, got, flood, c.want, c.flood)
		}
	}
}

// brief 4: a long retry_after must not hold the scheduler; the attempt is cut
// by its budget and the adapter's own wait takes over.
func TestAttemptBudgetCutsALongRetryAfter(t *testing.T) {
	old := attemptBudget
	attemptBudget = 500 * time.Millisecond
	t.Cleanup(func() { attemptBudget = old })

	long := reply{429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3600}}`}
	msg := okResult(`{"message_id":3,"chat":{"id":1,"type":"supergroup"},"date":1}`)
	a, api, clk := newAdapter(t, map[string][]reply{"sendMessage": {long, msg}}, Config{})
	id, err := a.SendHTML(context.Background(), 1, "x")
	if err != nil || id != 3 {
		t.Fatalf("SendHTML = %d, %v", id, err)
	}
	if fmt.Sprint(clk.slept) != fmt.Sprint([]time.Duration{61 * time.Second}) {
		t.Errorf("waits %v, want one wait of 61s", clk.slept)
	}
	if n := len(api.named("sendMessage")); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

// Doc 40-bot.md item 4 (flood wait): a caller that stops waiting ends the retry loop at once
// and the result is "not delivered", not an unknown outcome. Python had no cancellation.
func TestFloodWaitStopsWhenTheCallerGivesUp(t *testing.T) {
	a, api, _ := newAdapter(t, map[string][]reply{"sendMessage": {flood()}}, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	a.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	_, err := a.SendHTML(ctx, 1, "x")
	if !tg.IsTooManyRequests(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the 429 joined with the cancellation", err)
	}
	if n := len(api.named("sendMessage")); n != 1 {
		t.Errorf("%d requests after cancel", n)
	}
	// The scheduler must still read it as "not delivered".
	if a.OutcomeUnknown(err) {
		t.Error("a 429 joined with a cancellation was read as unknown")
	}
}

// brief 4: copyMessage with a caption into the same chat, silent.
func TestCopyWithCaption(t *testing.T) {
	a, api, clk := newAdapter(t, map[string][]reply{"copyMessage": {flood(), okResult(`{"message_id":901}`)}}, Config{})
	id, err := a.CopyWithCaption(context.Background(), -1001, 55, "<i>caption</i>")
	if err != nil || id != 901 {
		t.Fatalf("CopyWithCaption = %d, %v", id, err)
	}
	calls := api.named("copyMessage")
	if len(calls) != 2 || len(clk.slept) != 1 {
		t.Fatalf("%d calls, waits %v", len(calls), clk.slept)
	}
	b := calls[1].Body
	if b["chat_id"] != float64(-1001) || b["from_chat_id"] != float64(-1001) || b["message_id"] != float64(55) ||
		b["caption"] != "<i>caption</i>" || b["parse_mode"] != "HTML" || b["disable_notification"] != true {
		t.Errorf("body %v", b)
	}
	if _, has := b["reply_markup"]; has {
		t.Error("reply_markup was sent")
	}
}

// Doc 40-bot.md item 4 (copyMessage): scheduler.py read message_id from the aiogram
// result, which always has one; the raw answer is checked here, and one without it is an error.
func TestCopyWithoutAMessageIDIsAnUnexpectedResult(t *testing.T) {
	a, _, _ := newAdapter(t, map[string][]reply{"copyMessage": {okResult(`{}`)}}, Config{})
	_, err := a.CopyWithCaption(context.Background(), 1, 2, "c")
	if !errors.Is(err, tg.ErrUnexpectedResult) || !a.OutcomeUnknown(err) {
		t.Errorf("err = %v, unknown = %v", err, a.OutcomeUnknown(err))
	}
}

// tests/test_scheduler.py: pin_chat_message(disable_notification=True), no retry
func TestPinIsSilentAndNotRetried(t *testing.T) {
	a, api, clk := newAdapter(t, map[string][]reply{"pinChatMessage": {flood()}}, Config{})
	err := a.Pin(context.Background(), -1001, 55)
	if !tg.IsTooManyRequests(err) {
		t.Errorf("err = %v", err)
	}
	calls := api.named("pinChatMessage")
	if len(calls) != 1 || len(clk.slept) != 0 {
		t.Fatalf("%d calls, waits %v", len(calls), clk.slept)
	}
	b := calls[0].Body
	if b["chat_id"] != float64(-1001) || b["message_id"] != float64(55) || b["disable_notification"] != true {
		t.Errorf("body %v", b)
	}
}

// tests/test_scheduler.py: agreement reminder text, keyboard and wording
func TestSendAgreementReminder(t *testing.T) {
	cases := []struct {
		name     string
		lang     string
		outdated bool
		wantText string
		wantData string
	}{
		{"fresh group, English", "en", false, i18n.T("en", "agreement.reminder"), "agree:v:en"},
		{"changed agreement, Russian", "ru", true, i18n.T("ru", "agreement.updated_reminder"), "agree:v:ru"},
		{"unknown language falls back to English", "xx", false, i18n.T("en", "agreement.reminder"), "agree:v:en"},
		{"regional code is normalised", "uk-UA", true, i18n.T("uk", "agreement.updated_reminder"), "agree:v:uk"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, api, clk := newAdapter(t, map[string][]reply{"sendMessage": {flood()}}, Config{})
			err := a.SendAgreementReminder(context.Background(), -100, c.lang, c.outdated)
			if !tg.IsTooManyRequests(err) {
				t.Fatalf("err = %v (the reminder must not be retried)", err)
			}
			if len(clk.slept) != 0 {
				t.Errorf("waited %v", clk.slept)
			}
			b := api.named("sendMessage")[0].Body
			if b["text"] != c.wantText || b["disable_notification"] != true {
				t.Errorf("text %q, silent %v", b["text"], b["disable_notification"])
			}
			rows, _ := b["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
			if len(rows) != 1 {
				t.Fatalf("keyboard %v", b["reply_markup"])
			}
			button := rows[0].([]any)[0].(map[string]any)
			if button["callback_data"] != c.wantData {
				t.Errorf("callback_data %v, want %s", button["callback_data"], c.wantData)
			}
		})
	}
}

// Doc 40-bot.md item 1 (migration): getChat through tg.Call, the way the group migration handler
// reads the new supergroup.
func TestGetChat(t *testing.T) {
	a, api, _ := newAdapter(t, map[string][]reply{"getChat": {okResult(`{"id":-1002,"type":"supergroup","title":"New"}`)}}, Config{})
	chat, err := a.GetChat(context.Background(), -1002)
	if err != nil || chat.ID != -1002 || chat.Title != "New" || chat.Type != "supergroup" {
		t.Fatalf("GetChat = %+v, %v", chat, err)
	}
	if got := api.named("getChat")[0].Query.Get("chat_id"); got != "-1002" {
		t.Errorf("chat_id %q", got)
	}
	a2, _, _ := newAdapter(t, map[string][]reply{"getChat": {failure(400, "Bad Request: chat not found")}}, Config{})
	if _, err := a2.GetChat(context.Background(), 1); !a2.ChatNotFound(err) {
		t.Errorf("err = %v", err)
	}
}

// tests/test_scheduler.py::_delivery_outcome_unknown (timeout, network error, server error)
func TestOutcomeUnknown(t *testing.T) {
	// A real transport failure: the server drops the connection mid-request.
	dropping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer dropping.Close()
	var discard map[string]any
	transportErr := tg.New(testToken, tg.WithAPIBase(dropping.URL)).Call(context.Background(), "sendMessage", map[string]any{}, &discard)
	if transportErr == nil {
		t.Fatal("the dropped connection did not fail")
	}
	// A 2xx whose body cannot be read: Telegram did the work.
	a, _, _ := newAdapter(t, nil, Config{})
	client, _ := newAPI(t, map[string][]reply{"sendMessage": {{200, `{"ok":true,"result":"not a message"}`}}})
	var resp struct {
		Result tg.Message `json:"result"`
	}
	unreadable := client.Call(context.Background(), "sendMessage", map[string]any{}, &resp)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"500", &tg.APIError{StatusCode: 500, ErrorCode: 500}, true},
		{"502 from a proxy without a body", &tg.APIError{StatusCode: 502}, true},
		{"ok=false inside a 200 carrying a 5xx code", &tg.APIError{StatusCode: 200, ErrorCode: 503}, true},
		{"429 was refused, not lost", &tg.APIError{StatusCode: 429, ErrorCode: 429}, false},
		{"400", &tg.APIError{StatusCode: 400, ErrorCode: 400}, false},
		{"403", &tg.APIError{StatusCode: 403, ErrorCode: 403}, false},
		{"404", &tg.APIError{StatusCode: 404, ErrorCode: 404}, false},
		{"wrapped 500", fmt.Errorf("send: %w", &tg.APIError{StatusCode: 500}), true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("send: %w", context.DeadlineExceeded), true},
		{"cancelled", context.Canceled, true},
		{"dropped connection", transportErr, true},
		{"unreadable 2xx", unreadable, true},
		{"unexpected result", fmt.Errorf("%w: sendMessage", tg.ErrUnexpectedResult), true},
	}
	for _, c := range cases {
		if got := a.OutcomeUnknown(c.err); got != c.want {
			t.Errorf("%s: OutcomeUnknown = %v, want %v (%v)", c.name, got, c.want, c.err)
		}
	}
}

// tests/test_scheduler.py: "chat not found" in str(exc).lower() on a TelegramBadRequest
func TestChatNotFound(t *testing.T) {
	a, _, _ := newAdapter(t, nil, Config{})
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"Telegram's wording", &tg.APIError{StatusCode: 400, ErrorCode: 400, Description: "Bad Request: chat not found"}, true},
		{"any case", &tg.APIError{StatusCode: 400, ErrorCode: 400, Description: "Bad Request: CHAT NOT FOUND"}, true},
		{"wrapped", fmt.Errorf("remind: %w", &tg.APIError{StatusCode: 400, Description: "Bad Request: chat not found"}), true},
		{"another bad request", &tg.APIError{StatusCode: 400, ErrorCode: 400, Description: "Bad Request: message text is empty"}, false},
		{"only a 400 counts", &tg.APIError{StatusCode: 403, ErrorCode: 403, Description: "chat not found"}, false},
		{"not an api error", errors.New("chat not found"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := a.ChatNotFound(c.err); got != c.want {
			t.Errorf("%s: ChatNotFound = %v, want %v", c.name, got, c.want)
		}
	}
}

func alertBodies(api *fakeAPI) []map[string]any {
	var out []map[string]any
	for _, c := range api.named("sendMessage") {
		out = append(out, c.Body)
	}
	return out
}

// tests/test_utils.py::test_notify_developers_dedupes (same key within the cooldown)
func TestAlertDedupesAKeyForTenMinutes(t *testing.T) {
	a, api, clk := newAdapter(t, nil, Config{DeveloperIDs: []int64{11}})
	ctx := context.Background()
	a.Alert(ctx, "k", "boom")
	a.Alert(ctx, "k", "boom again")
	if n := len(api.named("sendMessage")); n != 1 {
		t.Fatalf("%d alerts, want 1", n)
	}
	clk.advance(alertCooldown - time.Second)
	a.Alert(ctx, "k", "still quiet")
	if n := len(api.named("sendMessage")); n != 1 {
		t.Fatalf("%d alerts just before the cooldown ended", n)
	}
	clk.advance(time.Second)
	a.Alert(ctx, "k", "again")
	if n := len(api.named("sendMessage")); n != 2 {
		t.Fatalf("%d alerts after the cooldown, want 2", n)
	}
	// A different key is independent.
	a.Alert(ctx, "other", "x")
	if n := len(api.named("sendMessage")); n != 3 {
		t.Errorf("%d alerts, want 3", n)
	}
}

// tests/test_utils.py (the message is the key when no dedupe key is given)
func TestAlertWithoutAKeyDedupesByText(t *testing.T) {
	a, api, _ := newAdapter(t, nil, Config{DeveloperIDs: []int64{11}})
	a.Alert(context.Background(), "", "same text")
	a.Alert(context.Background(), "", "same text")
	a.Alert(context.Background(), "", "other text")
	if n := len(api.named("sendMessage")); n != 2 {
		t.Errorf("%d alerts, want 2", n)
	}
}

// tests/test_utils.py::test_scrubs_secrets, test_scrubs_database_url_password
func TestAlertIsPlainTextWithSoundAndScrubbed(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "SUPERSECRET", "[redacted]") }
	a, api, _ := newAdapter(t, nil, Config{DeveloperIDs: []int64{11, 22}, Redact: redact})
	a.Alert(context.Background(), "k", "failed token=SUPERSECRET <b>&")
	bodies := alertBodies(api)
	if len(bodies) != 2 {
		t.Fatalf("%d messages, want one per developer", len(bodies))
	}
	for i, id := range []float64{11, 22} {
		b := bodies[i]
		if b["chat_id"] != id {
			t.Errorf("message %d went to %v", i, b["chat_id"])
		}
		if b["text"] != "failed token=[redacted] <b>&" {
			t.Errorf("text %q", b["text"])
		}
		if _, has := b["parse_mode"]; has {
			t.Errorf("an alert is plain text, got parse_mode %v", b["parse_mode"])
		}
		if v, has := b["disable_notification"]; has && v == true {
			t.Error("an alert must make a sound")
		}
	}
}

// tests/test_utils.py::test_notify_developers_skips_when_disabled
func TestAlertWithoutDevelopersDoesNothing(t *testing.T) {
	a, api, _ := newAdapter(t, nil, Config{})
	a.Alert(context.Background(), "k", "boom")
	if n := len(api.calls); n != 0 {
		t.Errorf("%d requests", n)
	}
}

// brief 4: an alert never fails the caller, and one developer failing does not
// stop the next one.
func TestAlertSurvivesFailures(t *testing.T) {
	a, api, _ := newAdapter(t, map[string][]reply{"sendMessage": {failure(403, "Forbidden: bot was blocked by the user"), okResult(`{"message_id":1,"chat":{"id":22,"type":"private"},"date":1}`)}},
		Config{DeveloperIDs: []int64{11, 22}})
	a.Alert(context.Background(), "k", "boom")
	if n := len(api.named("sendMessage")); n != 2 {
		t.Errorf("%d sends, want both developers tried", n)
	}
	// And a cancelled context does not panic or hang.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.Alert(ctx, "k2", "late")
}

// brief 4: the dedupe map is bounded.
func TestAlertKeyMapIsBounded(t *testing.T) {
	a, _, clk := newAdapter(t, nil, Config{DeveloperIDs: []int64{11}})
	for i := 0; i < 3*maxAlertKeys; i++ {
		a.Alert(context.Background(), fmt.Sprintf("key-%d", i), "x")
		clk.advance(time.Millisecond)
	}
	a.mu.Lock()
	n := len(a.alerted)
	a.mu.Unlock()
	if n > maxAlertKeys {
		t.Errorf("%d keys kept, bound is %d", n, maxAlertKeys)
	}
	// Once the old ones are quiet again they are dropped to make room.
	clk.advance(2 * alertCooldown)
	a.Alert(context.Background(), "fresh", "x")
	a.mu.Lock()
	n = len(a.alerted)
	a.mu.Unlock()
	if n > 2 {
		t.Errorf("%d keys kept after the cooldown passed for all", n)
	}
}

// Doc 40-bot.md item 4 (Alert): a message to the developers is cut to 3500 characters so it
// always fits one Telegram message; utils.notify_developers sent the text whole.
func TestAlertIsTruncated(t *testing.T) {
	a, api, _ := newAdapter(t, nil, Config{DeveloperIDs: []int64{11}})
	a.Alert(context.Background(), "k", strings.Repeat("я", 10000))
	text, _ := alertBodies(api)[0]["text"].(string)
	if n := len([]rune(text)); n > 4096 || !strings.HasSuffix(text, "…") {
		t.Errorf("alert has %d characters", n)
	}
}

// brief 4: the token never reaches an alert or a log line through an error text.
func TestTransportErrorsDoNotCarryTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()
	dropping := tg.New(testToken, tg.WithAPIBase(srv.URL))
	a := New(dropping, Config{DeveloperIDs: []int64{1}}, nil)
	_, err := a.SendHTML(context.Background(), 1, "x")
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN-VALUE") {
		t.Errorf("err = %v", err)
	}
}
