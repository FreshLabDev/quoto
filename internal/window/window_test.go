// SPDX-License-Identifier: Apache-2.0

package window

import (
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/FreshLabDev/quoto/internal/domain"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func day(y int, m time.Month, d int) time.Time { return utc(y, m, d, 0, 0) }

// doc §6: Windows. The expected instants were produced by Python's zoneinfo
// (datetime.combine(day, time, tzinfo=tz).astimezone(utc), fold=0); a one-off
// comparison over every zone with a change in 2025-2028, every quarter hour
// from the day before to the day after each change, found no difference.
func TestAtDaylightSavingRules(t *testing.T) {
	tests := []struct {
		name string
		zone string
		day  time.Time
		h, m int
		want time.Time
	}{
		{"Kyiv gap reads with the offset before the change", "Europe/Kyiv", day(2026, 3, 29), 3, 30, utc(2026, 3, 29, 1, 30)},
		{"Kyiv gap at its first minute", "Europe/Kyiv", day(2026, 3, 29), 3, 0, utc(2026, 3, 29, 1, 0)},
		{"Kyiv just after the gap", "Europe/Kyiv", day(2026, 3, 29), 4, 0, utc(2026, 3, 29, 1, 0)},
		{"Kyiv overlap takes the first occurrence", "Europe/Kyiv", day(2026, 10, 25), 3, 30, utc(2026, 10, 25, 0, 30)},
		{"Kyiv overlap at its first minute", "Europe/Kyiv", day(2026, 10, 25), 3, 0, utc(2026, 10, 25, 0, 0)},
		{"Kyiv overlap at its last minute", "Europe/Kyiv", day(2026, 10, 25), 3, 59, utc(2026, 10, 25, 0, 59)},
		{"Kyiv just after the overlap", "Europe/Kyiv", day(2026, 10, 25), 4, 0, utc(2026, 10, 25, 2, 0)},
		{"Berlin gap", "Europe/Berlin", day(2026, 3, 29), 2, 30, utc(2026, 3, 29, 1, 30)},
		{"Berlin overlap", "Europe/Berlin", day(2026, 10, 25), 2, 30, utc(2026, 10, 25, 0, 30)},
		{"New York gap", "America/New_York", day(2026, 3, 8), 2, 30, utc(2026, 3, 8, 7, 30)},
		{"New York overlap", "America/New_York", day(2026, 11, 1), 1, 30, utc(2026, 11, 1, 5, 30)},
		{"Lord Howe gap, a 30-minute change", "Australia/Lord_Howe", day(2026, 10, 4), 2, 15, utc(2026, 10, 3, 15, 45)},
		{"Lord Howe overlap, a 30-minute change", "Australia/Lord_Howe", day(2026, 4, 5), 1, 45, utc(2026, 4, 4, 14, 45)},
		{"ordinary day in winter", "Europe/Kyiv", day(2026, 1, 15), 21, 0, utc(2026, 1, 15, 19, 0)},
		{"ordinary day in summer", "Europe/Kyiv", day(2026, 7, 15), 21, 0, utc(2026, 7, 15, 18, 0)},
		{"zone without daylight saving", "Asia/Tokyo", day(2026, 6, 15), 21, 0, utc(2026, 6, 15, 12, 0)},
		{"UTC", "UTC", day(2026, 6, 15), 21, 0, utc(2026, 6, 15, 21, 0)},
		{"west of Greenwich", "America/New_York", day(2026, 6, 15), 21, 0, utc(2026, 6, 16, 1, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := At(tt.day, mustLoc(t, tt.zone), tt.h, tt.m); !got.Equal(tt.want) {
				t.Errorf("At(%s %02d:%02d %s) = %s, want %s", tt.day.Format("2006-01-02"), tt.h, tt.m, tt.zone, got.UTC(), tt.want)
			}
		})
	}
}

// tests/test_windows.py::test_dst_spring_forward_keeps_wall_clock_day
// plus the autumn counterpart and the two Kyiv changes of 2026.
func TestClosedForDayLength(t *testing.T) {
	tests := []struct {
		name  string
		zone  string
		qday  time.Time
		hour  int
		min   int
		start time.Time
		end   time.Time
		hours float64
	}{
		{"Berlin spring forward lasts 23 hours", "Europe/Berlin", day(2026, 3, 29), 21, 0, utc(2026, 3, 28, 20, 0), utc(2026, 3, 29, 19, 0), 23},
		{"Berlin fall back lasts 25 hours", "Europe/Berlin", day(2026, 10, 25), 21, 0, utc(2026, 10, 24, 19, 0), utc(2026, 10, 25, 20, 0), 25},
		{"Kyiv spring forward, cutoff in the gap", "Europe/Kyiv", day(2026, 3, 29), 3, 30, utc(2026, 3, 28, 1, 30), utc(2026, 3, 29, 1, 30), 24},
		{"Kyiv fall back, cutoff in the overlap", "Europe/Kyiv", day(2026, 10, 25), 3, 30, utc(2026, 10, 24, 0, 30), utc(2026, 10, 25, 0, 30), 24},
		{"ordinary day", "Europe/Kyiv", day(2026, 6, 10), 21, 0, utc(2026, 6, 9, 18, 0), utc(2026, 6, 10, 18, 0), 24},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := ClosedForDay(tt.qday, mustLoc(t, tt.zone), tt.hour, tt.min)
			if !w.QuoteDay.Equal(tt.qday) || !w.Start.Equal(tt.start) || !w.End.Equal(tt.end) {
				t.Fatalf("window = {%s %s %s}, want {%s %s %s}", w.QuoteDay.Format(time.DateOnly), w.Start, w.End,
					tt.qday.Format(time.DateOnly), tt.start, tt.end)
			}
			if got := w.End.Sub(w.Start).Hours(); got != tt.hours {
				t.Errorf("window is %v hours, want %v", got, tt.hours)
			}
			if w.Start.Location() != time.UTC || w.End.Location() != time.UTC {
				t.Errorf("window bounds must be UTC, got %s and %s", w.Start.Location(), w.End.Location())
			}
		})
	}
}

// tests/test_windows.py::test_window_endpoints_keep_local_cutoff_per_timezone
func TestClosedForDayKeepsLocalCutoffPerZone(t *testing.T) {
	tokyo := ClosedForDay(day(2026, 6, 15), mustLoc(t, "Asia/Tokyo"), 21, 0)
	ny := ClosedForDay(day(2026, 6, 15), mustLoc(t, "America/New_York"), 21, 0)
	if h := tokyo.End.In(mustLoc(t, "Asia/Tokyo")).Hour(); h != 21 {
		t.Errorf("Tokyo window ends at %d:00 local", h)
	}
	if h := ny.End.In(mustLoc(t, "America/New_York")).Hour(); h != 21 {
		t.Errorf("New York window ends at %d:00 local", h)
	}
	if tokyo.End.Equal(ny.End) {
		t.Error("the same local cutoff in two zones must be two different instants")
	}
}

func TestClosedForDayIgnoresTimeOfDayInQuoteDay(t *testing.T) {
	loc := mustLoc(t, "Europe/Kyiv")
	a := ClosedForDay(day(2026, 3, 27), loc, 21, 0)
	b := ClosedForDay(utc(2026, 3, 27, 23, 59), loc, 21, 0)
	if a != b {
		t.Errorf("windows differ: %+v vs %+v", a, b)
	}
}

// tests/test_windows.py::test_quote_day_before_cutoff_belongs_to_previous_day
// tests/test_windows.py::test_quote_day_after_cutoff_belongs_to_same_day
// The exact cutoff minute is not tested in Python; the code compares with >=.
func TestQuoteDayOf(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	kyiv := mustLoc(t, "Europe/Kyiv")
	tests := []struct {
		name string
		at   time.Time
		loc  *time.Location
		h, m int
		want time.Time
	}{
		{"one minute before the cutoff", time.Date(2026, 3, 27, 20, 59, 0, 0, berlin), berlin, 21, 0, day(2026, 3, 26)},
		{"one second before the cutoff", time.Date(2026, 3, 27, 20, 59, 59, 0, berlin), berlin, 21, 0, day(2026, 3, 26)},
		{"exactly at the cutoff", time.Date(2026, 3, 27, 21, 0, 0, 0, berlin), berlin, 21, 0, day(2026, 3, 27)},
		{"one minute after the cutoff", time.Date(2026, 3, 27, 21, 1, 0, 0, berlin), berlin, 21, 0, day(2026, 3, 27)},
		{"instant given in UTC is read in the zone", utc(2026, 3, 27, 20, 30), berlin, 21, 0, day(2026, 3, 27)},
		{"instant given in UTC, still before the local cutoff", utc(2026, 3, 27, 19, 30), berlin, 21, 0, day(2026, 3, 26)},
		{"just after midnight belongs to yesterday", time.Date(2026, 3, 28, 0, 5, 0, 0, kyiv), kyiv, 21, 0, day(2026, 3, 27)},
		{"cutoff in the Kyiv gap, read by wall clock", utc(2026, 3, 29, 1, 10), kyiv, 3, 30, day(2026, 3, 29)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QuoteDayOf(tt.at, tt.loc, tt.h, tt.m); !got.Equal(tt.want) {
				t.Errorf("QuoteDayOf = %s, want %s", got.Format(time.DateOnly), tt.want.Format(time.DateOnly))
			}
		})
	}
}

// tests/test_scheduler.py::test_quote_job_catches_up_after_scheduled_minute
// tests/test_scheduler.py::test_quote_job_catches_up_after_midnight_for_late_cutoff
// tests/test_scheduler.py::test_quote_job_ignores_stale_unprocessed_cutoff
// doc §6: Windows (catch-up window 2 h).
func TestDue(t *testing.T) {
	kyiv := mustLoc(t, "Europe/Kyiv")
	tests := []struct {
		name    string
		now     time.Time
		loc     *time.Location
		h, m    int
		wantDay time.Time // zero: not due
		wantEnd time.Time
	}{
		{"three minutes after a 21:00 cutoff", utc(2026, 3, 27, 19, 3), kyiv, 21, 0, day(2026, 3, 27), utc(2026, 3, 27, 19, 0)},
		{"exactly at the cutoff", utc(2026, 3, 27, 19, 0), kyiv, 21, 0, day(2026, 3, 27), utc(2026, 3, 27, 19, 0)},
		{"a minute before the cutoff", utc(2026, 3, 27, 18, 59), kyiv, 21, 0, time.Time{}, time.Time{}},
		{"last minute of the catch-up window", utc(2026, 3, 27, 20, 59), kyiv, 21, 0, day(2026, 3, 27), utc(2026, 3, 27, 19, 0)},
		{"two hours after is too late", utc(2026, 3, 27, 21, 0), kyiv, 21, 0, time.Time{}, time.Time{}},
		{"two hours and a minute after is too late", utc(2026, 3, 27, 21, 1), kyiv, 21, 0, time.Time{}, time.Time{}},
		{"late cutoff, due after midnight local time", utc(2026, 3, 27, 22, 15), kyiv, 23, 30, day(2026, 3, 27), utc(2026, 3, 27, 21, 30)},
		{"far from any cutoff", utc(2026, 3, 27, 6, 0), kyiv, 21, 0, time.Time{}, time.Time{}},
		// A cutoff in the gap: the wall clock reads 04:10 at 01:10 UTC, 40 minutes
		// "after" the 03:30 that never happened, although the instant of that
		// cutoff (01:30 UTC) is still ahead. Python subtracted wall-clock readings.
		{"cutoff in the gap is due by wall clock", utc(2026, 3, 29, 1, 10), kyiv, 3, 30, day(2026, 3, 29), utc(2026, 3, 29, 1, 30)},
		{"cutoff in the gap, before the wall clock reaches it", utc(2026, 3, 29, 0, 59), kyiv, 3, 30, time.Time{}, time.Time{}},
		// Fall back: at 01:10 UTC the wall clock reads 03:10 EET, 40 minutes after
		// the 02:30 cutoff, whose instant (EEST, before the overlap) is 23:30 UTC.
		{"fall back, cutoff before the overlap", utc(2026, 10, 25, 1, 10), kyiv, 2, 30, day(2026, 10, 25), utc(2026, 10, 24, 23, 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, ok := Due(tt.now, tt.loc, tt.h, tt.m)
			if tt.wantDay.IsZero() {
				if ok {
					t.Fatalf("due: %+v", w)
				}
				return
			}
			if !ok {
				t.Fatal("not due")
			}
			if !w.QuoteDay.Equal(tt.wantDay) || !w.End.Equal(tt.wantEnd) {
				t.Errorf("window = day %s end %s, want day %s end %s", w.QuoteDay.Format(time.DateOnly), w.End, tt.wantDay.Format(time.DateOnly), tt.wantEnd)
			}
		})
	}
}

func TestDueTriesTodayBeforeYesterday(t *testing.T) {
	// 23:30 cutoff, the minute tick at 00:15 local the next day: only
	// yesterday's cutoff is within the catch-up window.
	kyiv := mustLoc(t, "Europe/Kyiv")
	w, ok := Due(time.Date(2026, 3, 28, 0, 15, 0, 0, kyiv), kyiv, 23, 30)
	if !ok || !w.QuoteDay.Equal(day(2026, 3, 27)) {
		t.Fatalf("window %+v ok=%t", w, ok)
	}
	// A cutoff of 00:10 right after midnight: today's, not yesterday's.
	w, ok = Due(time.Date(2026, 3, 28, 0, 20, 0, 0, kyiv), kyiv, 0, 10)
	if !ok || !w.QuoteDay.Equal(day(2026, 3, 28)) {
		t.Fatalf("window %+v ok=%t", w, ok)
	}
}

func TestLastClosed(t *testing.T) {
	kyiv := mustLoc(t, "Europe/Kyiv")
	t.Run("today's cutoff has passed", func(t *testing.T) {
		w, today := LastClosed(utc(2026, 3, 27, 19, 30), kyiv, 21, 0)
		if !today || !w.QuoteDay.Equal(day(2026, 3, 27)) || !w.End.Equal(utc(2026, 3, 27, 19, 0)) {
			t.Errorf("window %+v today=%t", w, today)
		}
	})
	t.Run("long after the cutoff it is still today's window", func(t *testing.T) {
		w, today := LastClosed(utc(2026, 3, 27, 21, 59), kyiv, 21, 0)
		if !today || !w.QuoteDay.Equal(day(2026, 3, 27)) {
			t.Errorf("window %+v today=%t", w, today)
		}
	})
	t.Run("before today's cutoff the last closed window is yesterday's", func(t *testing.T) {
		w, today := LastClosed(utc(2026, 3, 27, 12, 0), kyiv, 21, 0)
		if today || !w.QuoteDay.Equal(day(2026, 3, 26)) || !w.End.Equal(utc(2026, 3, 26, 19, 0)) {
			t.Errorf("window %+v today=%t", w, today)
		}
	})
	t.Run("a late cutoff after midnight local time", func(t *testing.T) {
		// 00:15 local on the 28th, cutoff 23:30: the closed window is the 27th's
		// and its cutoff was not "today's" by the calendar.
		w, today := LastClosed(utc(2026, 3, 27, 22, 15), kyiv, 23, 30)
		if today || !w.QuoteDay.Equal(day(2026, 3, 27)) {
			t.Errorf("window %+v today=%t", w, today)
		}
	})
}

func TestDate(t *testing.T) {
	kyiv := mustLoc(t, "Europe/Kyiv")
	if got := Date(utc(2026, 3, 27, 22, 30), kyiv); !got.Equal(day(2026, 3, 28)) {
		t.Errorf("22:30 UTC is already the 28th in Kyiv, got %s", got.Format(time.DateOnly))
	}
	if got := Date(utc(2026, 3, 27, 22, 30), time.UTC); !got.Equal(day(2026, 3, 27)) {
		t.Errorf("got %s", got.Format(time.DateOnly))
	}
}

// The windows of consecutive days tile the timeline: a message belongs to one
// window only. Half-open [start, end) is what the SQL filters use.
func TestConsecutiveWindowsTileAcrossChanges(t *testing.T) {
	for _, zone := range []string{"Europe/Kyiv", "Europe/Berlin", "America/New_York", "Australia/Lord_Howe"} {
		loc := mustLoc(t, zone)
		for _, cutoff := range [][2]int{{21, 0}, {3, 30}, {2, 15}, {0, 0}, {23, 59}} {
			d := day(2026, 3, 1)
			prev := ClosedForDay(d, loc, cutoff[0], cutoff[1])
			for i := 1; i < 400; i++ {
				cur := ClosedForDay(d.AddDate(0, 0, i), loc, cutoff[0], cutoff[1])
				if !cur.Start.Equal(prev.End) {
					t.Fatalf("%s %02d:%02d: window of %s starts at %s, previous ended at %s", zone, cutoff[0], cutoff[1],
						cur.QuoteDay.Format(time.DateOnly), cur.Start, prev.End)
				}
				if !cur.End.After(cur.Start) {
					t.Fatalf("%s: empty window %+v", zone, cur)
				}
				prev = cur
			}
		}
	}
}

var _ = domain.Window{}
