// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/telegram"
)

const (
	testToken    = "123456:TEST-TOKEN"
	testUsername = "quoto_test_bot"
	groupID      = int64(-1001234567890)
	ownerID      = int64(777)
)

// apiCall is one request the fake Bot API saw.
type apiCall struct {
	Method string
	Body   map[string]any
	Query  url.Values
}

func (c apiCall) str(key string) string {
	s, _ := c.Body[key].(string)
	return s
}

// labels and data read the inline keyboard of a request.
func (c apiCall) keyboard() (labels, data []string) {
	markup, _ := c.Body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		for _, b := range row.([]any) {
			button := b.(map[string]any)
			labels = append(labels, button["text"].(string))
			d, _ := button["callback_data"].(string)
			data = append(data, d)
		}
	}
	return labels, data
}

type reply struct {
	status int
	body   string
}

func ok(result string) reply { return reply{200, `{"ok":true,"result":` + result + `}`} }

func fail(code int, description string) reply {
	return reply{code, fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, code, description)}
}

// fakeAPI is a Bot API server. Methods answer ok unless a handler is set.
type fakeAPI struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []apiCall
	handlers map[string]func(c apiCall, n int) reply
	// members maps a user id to a chat member status; absent means "member".
	members map[int64]string
	// memberFails makes getChatMember answer 400.
	memberFails bool
	URL         string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{t: t, handlers: map[string]func(apiCall, int) reply{}, members: map[int64]string{}}
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	api.URL = srv.URL
	return api
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	c := apiCall{Method: method, Query: r.URL.Query()}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.Body)
	}
	a.mu.Lock()
	n := 0
	for _, prev := range a.calls {
		if prev.Method == method {
			n++
		}
	}
	a.calls = append(a.calls, c)
	handler := a.handlers[method]
	a.mu.Unlock()

	var rep reply
	switch {
	case handler != nil:
		rep = handler(c, n)
	case method == "getChatMember":
		rep = a.chatMember(c)
	default:
		rep = ok("true")
	}
	if rep.status == -1 {
		// The handler wanted the connection dropped.
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

func (a *fakeAPI) chatMember(c apiCall) reply {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.memberFails {
		return fail(400, "Bad Request: chat not found")
	}
	user, _ := strconv.ParseInt(c.Query.Get("user_id"), 10, 64)
	status := a.members[user]
	if status == "" {
		status = "member"
	}
	member := map[string]any{"status": status, "user": map[string]any{"id": user, "is_bot": false, "first_name": "U"}}
	if status == "restricted-in" {
		member["status"], member["is_member"] = "restricted", true
	}
	raw, _ := json.Marshal(member)
	return ok(string(raw))
}

func (a *fakeAPI) on(method string, h func(c apiCall, n int) reply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handlers[method] = h
}

func (a *fakeAPI) answerWith(method string, r reply) {
	a.on(method, func(apiCall, int) reply { return r })
}

func (a *fakeAPI) setMember(user int64, status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.members[user] = status
}

func (a *fakeAPI) named(method string) []apiCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []apiCall
	for _, c := range a.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// methods lists the methods called, in order.
func (a *fakeAPI) methods() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.calls))
	for i, c := range a.calls {
		out[i] = c.Method
	}
	return out
}

func (a *fakeAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// fakeAdapter records what the bot sends, tells and asks of internal/telegram.
type fakeAdapter struct {
	mu      sync.Mutex
	sendCtx []error // ctx.Err() at the time of each Send
	sent    []telegram.SendParams
	alerts  []alertCall
	chats   map[int64]tg.Chat
	sendErr error
	chatErr error
	nextID  int64
}

type alertCall struct{ Key, Text string }

func (f *fakeAdapter) Send(ctx context.Context, p telegram.SendParams) (tg.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCtx = append(f.sendCtx, ctx.Err())
	if f.sendErr != nil {
		return tg.Message{}, f.sendErr
	}
	f.sent = append(f.sent, p)
	f.nextID++
	return tg.Message{MessageID: 9000 + f.nextID, Chat: tg.Chat{ID: p.ChatID}}, nil
}

func (f *fakeAdapter) GetChat(_ context.Context, id int64) (tg.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatErr != nil {
		return tg.Chat{}, f.chatErr
	}
	if c, ok := f.chats[id]; ok {
		return c, nil
	}
	return tg.Chat{ID: id, Type: "supergroup", Title: "New title"}, nil
}

func (f *fakeAdapter) Alert(_ context.Context, key, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, alertCall{key, text})
}

func (f *fakeAdapter) sentTo(chat int64) []telegram.SendParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []telegram.SendParams
	for _, p := range f.sent {
		if p.ChatID == chat {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeAdapter) alertKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := []string{}
	for _, a := range f.alerts {
		keys = append(keys, a.Key)
	}
	return keys
}

type claimKey struct {
	scope domain.LangScope
	id    int64
}

type ensureCall struct {
	Actor domain.Person
	Chat  domain.ChatInfo
}

type reactionCall struct {
	Chat, Message int64
	Counts        map[string]int
}

type migrationCall struct {
	Old, New int64
	Actor    domain.Person
	Chat     domain.ChatInfo
}

// store implements every store interface the bot uses, over maps, and keeps an
// ordered log of the writes so a test can say what happened and in what order.
type store struct {
	mu sync.Mutex

	// errs injects a failure into an operation by name.
	errs map[string]error

	touched  []domain.Person
	claims   map[claimKey]domain.LangClaim
	groups   map[int64]*domain.GroupSettings
	ensures  []ensureCall
	writes   []string
	accepted []string

	saveResults []error // consumed one per Save call, then success
	saveCalls   int
	saved       []domain.NewMessage
	savedAgain  map[[2]int64]bool
	edits       []domain.MessageEdit
	nextDBID    int64

	deltas    []reactionCall
	absolutes []reactionCall

	quotes     map[int64]*domain.QuoteDetail
	quoteReads []int64
	chatStats  *domain.ChatStats
	userStats  *domain.UserStats

	migrations []migrationCall
	migrated   bool

	// clearLeaves is a claim another bot holds: ClearLanguage withdraws Quoto's
	// own claim, and this one is what core resolves afterwards.
	clearLeaves map[claimKey]domain.LangClaim
	// noGroup makes the group writes report that there is no such group.
	noGroup bool
	// onTouch runs at the start of TouchUser, outside the store's lock.
	onTouch func(p domain.Person)
	// getMissing makes Get report that the group has no row.
	getMissing bool
}

func newStore() *store {
	return &store{
		errs:       map[string]error{},
		claims:     map[claimKey]domain.LangClaim{},
		groups:     map[int64]*domain.GroupSettings{},
		savedAgain: map[[2]int64]bool{},
		quotes:     map[int64]*domain.QuoteDetail{},
		migrated:   true,
	}
}

func (s *store) fail(op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errs[op]
}

func (s *store) log(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, fmt.Sprintf(format, args...))
}

func (s *store) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// group returns the stored row for a test to set up or read.
func (s *store) group(id int64) *domain.GroupSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		minute := 7
		g = &domain.GroupSettings{ChatID: id, IsActive: true, QuoteMinute: &minute}
		s.groups[id] = g
	}
	return g
}

// --- domain.Core ---

func (s *store) TouchUser(_ context.Context, p domain.Person) error {
	if s.onTouch != nil {
		s.onTouch(p)
	}
	if err := s.fail("TouchUser"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched = append(s.touched, p)
	return nil
}

func (s *store) SetLanguage(_ context.Context, scope domain.LangScope, subject int64, code string, source domain.LangSource) error {
	if err := s.fail("SetLanguage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[claimKey{scope, subject}] = domain.LangClaim{Language: code, Source: source}
	s.writes = append(s.writes, fmt.Sprintf("SetLanguage %s %d %s %s", scope, subject, code, source))
	return nil
}

func (s *store) ClearLanguage(_ context.Context, scope domain.LangScope, subject int64) error {
	if err := s.fail("ClearLanguage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claims, claimKey{scope, subject})
	if left, ok := s.clearLeaves[claimKey{scope, subject}]; ok {
		s.claims[claimKey{scope, subject}] = left
	}
	s.writes = append(s.writes, fmt.Sprintf("ClearLanguage %s %d", scope, subject))
	return nil
}

func (s *store) LanguageClaim(_ context.Context, scope domain.LangScope, subject int64) (domain.LangClaim, error) {
	if err := s.fail("LanguageClaim"); err != nil {
		return domain.LangClaim{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims[claimKey{scope, subject}], nil
}

func (s *store) ClaimChatLanguageAuto(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (s *store) DisplayNames(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

// --- domain.Groups ---

func (s *store) Ensure(_ context.Context, actor domain.Person, chat domain.ChatInfo) (domain.GroupSettings, error) {
	if err := s.fail("Ensure"); err != nil {
		return domain.GroupSettings{}, err
	}
	g := s.group(chat.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensures = append(s.ensures, ensureCall{actor, chat})
	g.IsActive = true
	return *g, nil
}

func (s *store) Get(_ context.Context, chatID int64) (domain.GroupSettings, bool, error) {
	if err := s.fail("Get"); err != nil {
		return domain.GroupSettings{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[chatID]
	if !ok || s.getMissing {
		return domain.GroupSettings{}, false, nil
	}
	out := *g
	out.Name = "Test group"
	if claim, has := s.claims[claimKey{domain.ScopeChat, chatID}]; has {
		out.Language, out.LanguageSource = claim.Language, claim.Source
	}
	return out, true, nil
}

func (s *store) ListActive(context.Context) ([]domain.GroupSettings, error) { return nil, nil }

func (s *store) SetActive(_ context.Context, chatID int64, active bool) (bool, error) {
	if err := s.fail("SetActive"); err != nil {
		return false, err
	}
	g := s.group(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	g.IsActive = active
	s.writes = append(s.writes, fmt.Sprintf("SetActive %d %v", chatID, active))
	return true, nil
}

func (s *store) SetTimezone(_ context.Context, chatID int64, tz string) (bool, error) {
	if err := s.fail("SetTimezone"); err != nil {
		return false, err
	}
	g := s.group(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noGroup {
		return false, nil
	}
	g.Timezone = &tz
	s.writes = append(s.writes, fmt.Sprintf("SetTimezone %d %s", chatID, tz))
	return true, nil
}

func (s *store) SetTimezoneAuto(context.Context, int64, string) (bool, error) { return false, nil }

func (s *store) AdjustQuoteTime(_ context.Context, chatID int64, delta int) (bool, error) {
	if err := s.fail("AdjustQuoteTime"); err != nil {
		return false, err
	}
	g := s.group(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	h, m := g.QuoteTime()
	h, m = domain.NormalizeQuoteTime(h, m+delta)
	g.QuoteHour, g.QuoteMinute = &h, &m
	s.writes = append(s.writes, fmt.Sprintf("AdjustQuoteTime %d %d", chatID, delta))
	return true, nil
}

func (s *store) AdjustMinMessages(_ context.Context, chatID int64, delta int) (bool, error) {
	if err := s.fail("AdjustMinMessages"); err != nil {
		return false, err
	}
	g := s.group(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	v := domain.ClampMinMessages(g.MinMessagesEffective() + delta)
	g.MinMessages = &v
	s.writes = append(s.writes, fmt.Sprintf("AdjustMinMessages %d %d", chatID, delta))
	return true, nil
}

func (s *store) Toggle(_ context.Context, chatID int64, key domain.ToggleKey) (bool, error) {
	if err := s.fail("Toggle"); err != nil {
		return false, err
	}
	g := s.group(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	next := !g.Enabled(key)
	switch key {
	case domain.ToggleContext:
		g.QuoteContext = &next
	case domain.ToggleBoring:
		g.BoringNotice = &next
	case domain.TogglePin:
		g.Pin = &next
	case domain.ToggleMedia:
		g.MediaAnalysis = &next
	}
	s.writes = append(s.writes, fmt.Sprintf("Toggle %d %s", chatID, key))
	return true, nil
}

func (s *store) AcceptAgreement(_ context.Context, chatID, userID int64, language string) (bool, error) {
	if err := s.fail("AcceptAgreement"); err != nil {
		return false, err
	}
	g := s.group(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = append(s.accepted, fmt.Sprintf("%d %d %s", chatID, userID, language))
	s.writes = append(s.writes, fmt.Sprintf("AcceptAgreement %d %d %s", chatID, userID, language))
	if !g.AgreementAccepted() {
		at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
		version := domain.DocumentVersion
		g.AgreementAcceptedAt, g.AgreementAcceptedBy, g.AgreementLanguage, g.AgreementVersion = &at, &userID, &language, &version
	}
	return true, nil
}

func (s *store) MigrateChatID(_ context.Context, oldID, newID int64, actor domain.Person, newChat domain.ChatInfo) (bool, error) {
	if err := s.fail("MigrateChatID"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.migrations = append(s.migrations, migrationCall{oldID, newID, actor, newChat})
	return s.migrated, nil
}

// --- domain.Messages ---

func (s *store) Save(_ context.Context, m domain.NewMessage) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	if len(s.saveResults) > 0 {
		err := s.saveResults[0]
		s.saveResults = s.saveResults[1:]
		if err != nil {
			return 0, false, err
		}
	}
	key := [2]int64{m.ChatID, m.MessageID}
	if s.savedAgain[key] {
		return 0, false, nil
	}
	s.savedAgain[key] = true
	s.saved = append(s.saved, m)
	s.nextDBID++
	return s.nextDBID, true, nil
}

func (s *store) Edit(_ context.Context, e domain.MessageEdit) error {
	if err := s.fail("Edit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edits = append(s.edits, e)
	return nil
}

func (s *store) CountWindow(context.Context, int64, domain.Window) (int, error)  { return 0, nil }
func (s *store) DeleteWindow(context.Context, int64, domain.Window) (int, error) { return 0, nil }
func (s *store) ListWindow(context.Context, int64, domain.Window) ([]domain.WindowMessage, error) {
	return nil, nil
}
func (s *store) DeleteOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }

// --- domain.Reactions ---

func (s *store) SyncAbsolute(_ context.Context, chatID, messageID int64, counts map[string]int) error {
	if err := s.fail("SyncAbsolute"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.absolutes = append(s.absolutes, reactionCall{chatID, messageID, counts})
	return nil
}

func (s *store) ApplyDelta(_ context.Context, chatID, messageID int64, deltas map[string]int) error {
	if err := s.fail("ApplyDelta"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deltas = append(s.deltas, reactionCall{chatID, messageID, deltas})
	return nil
}

// --- domain.Quotes (only Detail is used) ---

func (s *store) Detail(_ context.Context, id int64, publicOnly bool) (*domain.QuoteDetail, error) {
	if err := s.fail("Detail"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quoteReads = append(s.quoteReads, id)
	if !publicOnly {
		panic("the bot must ask for public quotes only")
	}
	d := s.quotes[id]
	if d == nil {
		return nil, nil
	}
	copied := *d
	return &copied, nil
}

// --- domain.StatsReader ---

func (s *store) Chat(context.Context, int64) (*domain.ChatStats, error) {
	if err := s.fail("Chat"); err != nil {
		return nil, err
	}
	return s.chatStats, nil
}

func (s *store) User(context.Context, int64, int64) (*domain.UserStats, error) {
	if err := s.fail("User"); err != nil {
		return nil, err
	}
	return s.userStats, nil
}

// storeWithQuotes lets the embedded interface satisfy domain.Quotes without
// implementing every method; calling one the bot should not use panics.
type quotesStore struct {
	domain.Quotes
	*store
}

func (q quotesStore) Detail(ctx context.Context, id int64, publicOnly bool) (*domain.QuoteDetail, error) {
	return q.store.Detail(ctx, id, publicOnly)
}

type fakeMedia struct {
	mu  sync.Mutex
	ids []int64
}

func (m *fakeMedia) Submit(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ids = append(m.ids, id)
}

func (m *fakeMedia) submitted() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int64(nil), m.ids...)
}

type fakeCatchUp struct {
	mu    sync.Mutex
	chats []int64
	// done is closed-over by tests that wait for the goroutine.
	called chan int64
	panics bool
}

func (c *fakeCatchUp) CatchUp(_ context.Context, chatID int64) {
	c.mu.Lock()
	c.chats = append(c.chats, chatID)
	c.mu.Unlock()
	if c.called != nil {
		c.called <- chatID
	}
	if c.panics {
		panic("catch-up exploded")
	}
}

func (c *fakeCatchUp) calls() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.chats...)
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

func (c *clock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// harness is a bot wired to fakes.
type harness struct {
	t       *testing.T
	bot     *Bot
	api     *fakeAPI
	store   *store
	adapter *fakeAdapter
	media   *fakeMedia
	catchup *fakeCatchUp
	clock   *clock
	signer  deeplink.Signer
}

func newHarness(t *testing.T, cfgs ...func(*Config)) *harness {
	t.Helper()
	api := newFakeAPI(t)
	st := newStore()
	h := &harness{
		t: t, api: api, store: st,
		adapter: &fakeAdapter{},
		media:   &fakeMedia{},
		catchup: &fakeCatchUp{},
		clock:   &clock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)},
		signer:  deeplink.NewSigner(testToken),
	}
	cfg := Config{BotUsername: "@" + testUsername, MediaAnalysisEnabled: true, Version: "1.2.3"}
	for _, f := range cfgs {
		f(&cfg)
	}
	h.bot = New(cfg, Deps{
		TG:        tg.New(testToken, tg.WithAPIBase(api.URL), tg.WithAllowedUpdates(AllowedUpdates...)),
		Adapter:   h.adapter,
		Core:      st,
		Groups:    st,
		Messages:  st,
		Reactions: st,
		Quotes:    quotesStore{store: st},
		Stats:     st,
		Media:     h.media,
		CatchUp:   h.catchup,
		Signer:    h.signer,
	})
	h.bot.now, h.bot.sleep = h.clock.Now, h.clock.Sleep
	return h
}

// deliver hands one update to the bot as the poll loop would, and waits for it.
func (h *harness) deliver(u tg.Update) {
	h.t.Helper()
	h.bot.handle(context.Background(), u)
}

func (h *harness) message(m tg.Message) { h.t.Helper(); h.deliver(tg.Update{UpdateID: 1, Message: &m}) }

// press delivers a button press from user on the panel message.
func (h *harness) press(user tg.User, panel tg.Message, data string) {
	h.t.Helper()
	h.deliver(tg.Update{UpdateID: 1, Callback: &tg.CallbackQuery{ID: "cb", From: user, Message: panel, Data: data}})
}

// panelMessage is a group panel (message 50) as a button press carries it.
func panelMessage() tg.Message {
	return tg.Message{MessageID: 50, Date: 1700000000, Chat: groupChat()}
}

func groupChat() tg.Chat {
	return tg.Chat{ID: groupID, Type: tg.ChatSupergroup, Title: "Test group"}
}

func privatePanel() tg.Message {
	return tg.Message{MessageID: 50, Date: 1700000000, Chat: tg.Chat{ID: ownerID, Type: tg.ChatPrivate}}
}

func person(id int64, lang string) tg.User {
	return tg.User{ID: id, FirstName: "Person", LanguageCode: lang}
}

func groupText(from tg.User, id int64, text string) tg.Message {
	return tg.Message{MessageID: id, Date: 1700000000, From: &from, Chat: groupChat(), Text: text}
}

// edit is the last editMessageText request.
type edit struct {
	apiCall
}

func (h *harness) lastEdit() edit {
	h.t.Helper()
	edits := h.api.named("editMessageText")
	if len(edits) == 0 {
		h.t.Fatal("the panel was not edited")
	}
	return edit{edits[len(edits)-1]}
}

func (e edit) text() string { return e.str("text") }

// answers returns the answerCallbackQuery requests.
func (h *harness) answers() []apiCall { return h.api.named("answerCallbackQuery") }

// oneAnswer fails the test unless the press was answered exactly once, and
// returns that answer.
func (h *harness) oneAnswer() (text string, alert bool) {
	h.t.Helper()
	got := h.answers()
	if len(got) != 1 {
		h.t.Fatalf("%d answers, want exactly one", len(got))
	}
	a := got[0]
	alert, _ = a.Body["show_alert"].(bool)
	return a.str("text"), alert
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func errBoom() error { return errors.New("boom") }

// labelsOf flattens an inline keyboard into its button labels.
func labelsOf(m *tg.InlineKeyboardMarkup) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

// dataOf flattens an inline keyboard into its callback_data.
func dataOf(m *tg.InlineKeyboardMarkup) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

// onlySent fails the test unless exactly one message went to the chat.
func (h *harness) onlySent(chat int64) telegram.SendParams {
	h.t.Helper()
	got := h.adapter.sentTo(chat)
	if len(got) != 1 {
		h.t.Fatalf("%d messages sent to chat %d, want 1: %+v", len(got), chat, got)
	}
	return got[0]
}

// editedLabels is the keyboard of the last panel edit.
func (e edit) labels() []string {
	l, _ := e.keyboard()
	return l
}

func (e edit) data() []string {
	_, d := e.keyboard()
	return d
}

type boomError string

func (e boomError) Error() string { return string(e) }

func errBoomWith(msg string) error { return boomError(msg) }
