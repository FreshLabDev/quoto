// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

func privateStartJSON(updateID, userID int64) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":1700000000,`+
		`"from":{"id":%d,"is_bot":false,"first_name":"P","language_code":"en"},`+
		`"chat":{"id":%d,"type":"private"},"text":"/start"}}`, updateID, updateID, userID, userID)
}

func groupTextJSON(updateID, messageID int64, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":1700000000,`+
		`"from":{"id":777,"is_bot":false,"first_name":"P"},`+
		`"chat":{"id":%d,"type":"supergroup","title":"Test group"},"text":%q}}`, updateID, messageID, groupID, text)
}

// scriptPolling makes getUpdates answer the given JSON batches in order and then
// wait until the test ends. Each call announces itself on arrived with its
// number. The confirming call Run makes on the way out (timeout=0) is answered
// at once.
func (h *harness) scriptPolling(batches ...string) (arrived <-chan int) {
	h.t.Helper()
	stop := make(chan struct{})
	h.t.Cleanup(func() { close(stop) })
	calls := make(chan int, 1024)
	h.api.on("getUpdates", func(c apiCall, n int) reply {
		if c.Query.Get("timeout") == "0" {
			return ok("[]")
		}
		calls <- n
		if n < len(batches) {
			return ok(batches[n])
		}
		<-stop
		return ok("[]")
	})
	return calls
}

func (h *harness) runAsync(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.bot.Run(ctx) }()
	return done
}

// waitFor receives until the channel has produced n.
func waitFor(t *testing.T, c <-chan int, n int) {
	t.Helper()
	for got := range c {
		if got >= n {
			return
		}
	}
}

func getUpdatesCalls(h *harness) []apiCall { return h.api.named("getUpdates") }

// The update loop: the parameters of getUpdates, the offset that moves past each
// accepted update, the handling of what arrived, the final confirmation, and the
// time of the last good poll for /healthz.
func TestRunPollsAndHandlesUpdates(t *testing.T) {
	h := newHarness(t)
	arrived := h.scriptPolling(
		"["+privateStartJSON(10, ownerID)+","+groupTextJSON(11, 5, "hello")+","+`{"update_id":12,"inline_query":{"id":"1"}}`+"]",
		"[]",
	)
	if !h.bot.LastPoll().IsZero() {
		t.Fatal("LastPoll before the first poll must be the zero time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 2) // the third call: both batches were taken
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	calls := getUpdatesCalls(h)
	if len(calls) < 4 {
		t.Fatalf("%d getUpdates calls", len(calls))
	}
	first := calls[0].Query
	if first.Get("timeout") != "10" {
		t.Errorf("timeout %q, want 10", first.Get("timeout"))
	}
	var allowed []string
	if err := json.Unmarshal([]byte(first.Get("allowed_updates")), &allowed); err != nil || !reflect.DeepEqual(allowed, AllowedUpdates) {
		t.Errorf("allowed_updates %q (%v)", first.Get("allowed_updates"), err)
	}
	if first.Get("offset") != "" {
		t.Errorf("first call has offset %q", first.Get("offset"))
	}
	if calls[1].Query.Get("offset") != "13" {
		t.Errorf("second call offset %q, want one past the last update (13)", calls[1].Query.Get("offset"))
	}
	last := calls[len(calls)-1].Query
	if last.Get("timeout") != "0" || last.Get("offset") != "13" {
		t.Errorf("confirmation timeout=%q offset=%q, want 0 and 13", last.Get("timeout"), last.Get("offset"))
	}

	if sent := h.adapter.sentTo(ownerID); len(sent) != 1 {
		t.Errorf("the private /start was not answered: %+v", h.adapter.sent)
	}
	if len(h.store.saved) != 1 || h.store.saved[0].Text != "hello" {
		t.Errorf("the group message was not collected: %+v", h.store.saved)
	}
	if got := h.bot.LastPoll(); !got.Equal(h.clock.Now()) {
		t.Errorf("LastPoll = %v, want %v", got, h.clock.Now())
	}
	if keys := h.adapter.alertKeys(); len(keys) != 0 {
		t.Errorf("alerts %v", keys)
	}
}

// Before it polls, Run asks about rich Markdown (a self-hosted server only) and
// publishes the commands: 35 calls (CMD-01..06).
func TestRunStartupOrder(t *testing.T) {
	h := newHarness(t, selfHosted)
	arrived := h.scriptPolling()
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 0)
	cancel()
	<-done

	methods := h.api.methods()
	if methods[0] != "sendRichMessage" {
		t.Fatalf("first call %q, want the probe", methods[0])
	}
	commands := 0
	for i, m := range methods {
		if m == "setMyCommands" {
			commands++
			if i == 0 || commands > 35 {
				t.Fatalf("unexpected commands call at %d", i)
			}
		}
		if m == "getUpdates" && commands != 35 {
			t.Fatalf("polling began after %d of 35 command calls", commands)
		}
	}
	if commands != 35 {
		t.Fatalf("%d commands calls", commands)
	}
	if !h.bot.rich.available() {
		t.Fatal("the probe's verdict (the method answered) was not kept")
	}
}

// A failing poll backs off 1 s, 2 s, 3 s ... up to 30 s, and recovers silently
// into the next success; the loop never stops by itself.
func TestRunBackoff(t *testing.T) {
	h := newHarness(t)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	arrived := make(chan int, 1024)
	const failures = 36
	h.api.on("getUpdates", func(c apiCall, n int) reply {
		if c.Query.Get("timeout") == "0" {
			return ok("[]")
		}
		arrived <- n
		if n < failures {
			return fail(409, "Conflict: terminated by other getUpdates request")
		}
		<-stop
		return ok("[]")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, failures)
	cancel()
	<-done

	waits := h.clock.waits()
	if len(waits) != failures {
		t.Fatalf("%d waits, want %d", len(waits), failures)
	}
	for i, w := range waits {
		want := min(time.Duration(i+1)*time.Second, 30*time.Second)
		if w != want {
			t.Fatalf("wait %d is %v, want %v", i+1, w, want)
		}
	}
	if !h.bot.LastPoll().IsZero() {
		t.Fatal("LastPoll moved although no poll succeeded")
	}
}

// The failure count restarts after a success.
func TestRunBackoffResets(t *testing.T) {
	h := newHarness(t)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	arrived := make(chan int, 64)
	h.api.on("getUpdates", func(c apiCall, n int) reply {
		if c.Query.Get("timeout") == "0" {
			return ok("[]")
		}
		arrived <- n
		switch n {
		case 0, 1:
			return fail(409, "Conflict")
		case 2:
			return ok("[]")
		case 3:
			return fail(409, "Conflict")
		}
		<-stop
		return ok("[]")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 4)
	cancel()
	<-done
	if got, want := h.clock.waits(), []time.Duration{time.Second, 2 * time.Second, time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
}

// A handler that fails or panics costs that update only: the developers are told
// once per kind, polling goes on, and the next update is handled.
func TestRunSurvivesFailingHandlers(t *testing.T) {
	h := newHarness(t)
	h.store.onTouch = func(p domain.Person) {
		if p.ID == 666 {
			panic("handler exploded")
		}
	}
	h.store.errs["Ensure"] = errBoom()
	arrived := h.scriptPolling(
		"["+privateStartJSON(1, 666)+","+privateStartJSON(2, 667)+","+groupTextJSON(3, 9, "a")+","+groupTextJSON(4, 10, "b")+"]",
		"[]",
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 2)
	cancel()
	<-done

	if sent := h.adapter.sentTo(667); len(sent) != 1 {
		t.Fatalf("the update after the panic was not handled: %+v", h.adapter.sent)
	}
	// Two failing group messages and one panic, all of kind "message": the adapter
	// is told each time and does the deduplication (telegram.Client tests).
	keys := h.adapter.alertKeys()
	if len(keys) != 3 {
		t.Fatalf("alerts %v", keys)
	}
	for _, k := range keys {
		if k != "update:message" {
			t.Fatalf("alert key %q", k)
		}
	}
	if !strings.Contains(h.adapter.alerts[0].Text+h.adapter.alerts[1].Text+h.adapter.alerts[2].Text, "handler exploded") {
		t.Fatalf("the panic is not in the alerts: %+v", h.adapter.alerts)
	}
}

// gate lets a test hold handlers inside TouchUser and count how many are in there.
type gate struct {
	entered chan int64
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func newGate() *gate { return &gate{entered: make(chan int64, 256), release: make(chan struct{}, 256)} }

func (g *gate) hook(p domain.Person) {
	n := g.active.Add(1)
	for {
		old := g.peak.Load()
		if n <= old || g.peak.CompareAndSwap(old, n) {
			break
		}
	}
	g.entered <- p.ID
	<-g.release
	g.active.Add(-1)
}

func (g *gate) open(n int) {
	for i := 0; i < n; i++ {
		g.release <- struct{}{}
	}
}

func idle() {
	for i := 0; i < 200; i++ {
		runtime.Gosched()
	}
}

func startBatch(first, count int) string {
	var parts []string
	for i := 0; i < count; i++ {
		parts = append(parts, privateStartJSON(int64(first+i), int64(1000+first+i)))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// At most sixteen updates are handled at once; the seventeenth waits for a free
// place, and the offset moves only past updates that were taken.
func TestRunBoundsConcurrencyAndOffset(t *testing.T) {
	h := newHarness(t)
	g := newGate()
	h.store.onTouch = g.hook
	arrived := h.scriptPolling(startBatch(1, 40))
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 0)
	for i := 0; i < workers; i++ {
		<-g.entered
	}
	idle()
	select {
	case id := <-g.entered:
		t.Fatalf("handler %d started with %d already running", id, workers)
	default:
	}
	if got := g.active.Load(); got != workers {
		t.Fatalf("%d handlers running, want %d", got, workers)
	}

	// Shutdown while the pool is full: Run waits for the handlers it has.
	cancel()
	idle()
	select {
	case <-done:
		t.Fatal("Run returned while handlers were still working")
	default:
	}
	g.open(40)
	<-done

	if got := g.peak.Load(); got != workers {
		t.Fatalf("peak concurrency %d, want %d", got, workers)
	}
	// Sixteen were taken (ids 1..16), so everything after update 16 is delivered again.
	last := getUpdatesCalls(h)
	confirm := last[len(last)-1].Query
	if confirm.Get("timeout") != "0" || confirm.Get("offset") != "17" {
		t.Fatalf("confirmation timeout=%q offset=%q, want 0 and 17", confirm.Get("timeout"), confirm.Get("offset"))
	}
	// Handlers keep working after the run's context is gone.
	for i, err := range h.adapter.sendCtx {
		if err != nil {
			t.Fatalf("send %d ran with a canceled context: %v", i, err)
		}
	}
	if len(h.adapter.sent) != workers {
		t.Fatalf("%d panels sent, want %d", len(h.adapter.sent), workers)
	}
}

// A place freed by a finished handler goes to the next update.
func TestRunNextUpdateTakesAFreedPlace(t *testing.T) {
	h := newHarness(t)
	g := newGate()
	h.store.onTouch = g.hook
	arrived := h.scriptPolling(startBatch(1, workers+1))
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 0)
	for i := 0; i < workers; i++ {
		<-g.entered
	}
	g.open(1)
	if id := <-g.entered; id != int64(1000+workers+1) {
		t.Fatalf("the update that took the place was %d", id)
	}
	g.open(workers)
	cancel()
	<-done
	confirm := getUpdatesCalls(h)
	if got := confirm[len(confirm)-1].Query.Get("offset"); got != strconv.Itoa(workers+2) {
		t.Fatalf("confirmed offset %s, want %d", got, workers+2)
	}
}

// With nothing accepted there is nothing to confirm.
func TestRunWithoutUpdatesDoesNotConfirm(t *testing.T) {
	h := newHarness(t)
	arrived := h.scriptPolling("[]")
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 1)
	cancel()
	<-done
	for _, c := range getUpdatesCalls(h) {
		if c.Query.Get("timeout") == "0" {
			t.Fatalf("a confirmation with offset %q was sent", c.Query.Get("offset"))
		}
	}
}

// A context that is already done ends Run at once, without polling.
func TestRunWithACanceledContext(t *testing.T) {
	h := newHarness(t)
	h.scriptPolling()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.bot.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(getUpdatesCalls(h)); n != 0 {
		t.Fatalf("%d getUpdates calls", n)
	}
}

// The catch-up that an acceptance starts is waited for on shutdown: it is counted
// with the handlers.
func TestRunWaitsForTheCatchUp(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	started := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	h.bot.d.CatchUp = catchUpFunc(func(context.Context, int64) {
		once.Do(func() { close(started) })
		<-finish
	})
	cb := fmt.Sprintf(`{"update_id":1,"callback_query":{"id":"c1","from":{"id":777,"is_bot":false,"first_name":"P","language_code":"ru"},`+
		`"message":{"message_id":50,"date":1700000000,"chat":{"id":%d,"type":"supergroup","title":"Test group"}},"data":"agree:a:ru:g:777"}}`, groupID)
	arrived := h.scriptPolling("[" + cb + "]")
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 1)
	<-started
	cancel()
	idle()
	select {
	case <-done:
		t.Fatal("Run returned while the catch-up was still going")
	default:
	}
	close(finish)
	<-done
	if len(h.store.accepted) != 1 {
		t.Fatalf("accepted %v", h.store.accepted)
	}
}
