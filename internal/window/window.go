// SPDX-License-Identifier: Apache-2.0

// Package window is the arithmetic of a group's day: when its cutoff falls in
// the group's time zone, which messages belong to the day that cutoff closes,
// and whether it is time to process it.
//
// Everything here is a pure function of an instant, a zone and a cutoff time.
// The rules are those of the Python version (zoneinfo with fold=0, aware
// datetimes that share a tzinfo), written out instead of inherited from
// time.Date, whose behaviour around a daylight-saving change is documented as
// "one of the two, not guaranteed which":
//
//   - A day is the span between two consecutive cutoffs on the wall clock, so a
//     day with a clock change lasts 23 or 25 hours, never "minus 24 hours".
//   - A cutoff that falls into the gap of a spring-forward change does not
//     exist; it is read with the offset in force before the change.
//   - A cutoff that occurs twice at an autumn change is the first occurrence.
//   - How long ago a cutoff was is the difference of two wall-clock readings,
//     not of two instants. The two differ only for a cutoff within a couple of
//     hours of a change, and the Python scheduler used the wall-clock one.
package window

import (
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// CatchUp is how long after its cutoff a group's day can still be started by
// the minute tick. A longer outage skips that day, and its messages wait for
// the retention job.
const CatchUp = 2 * time.Hour

// Date returns the calendar date of t in loc as midnight UTC, the form
// domain.Window.QuoteDay and the quote_day column use.
func Date(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// At is the instant of the wall-clock time hour:minute on day in loc. day is
// read for its year, month and day only.
func At(day time.Time, loc *time.Location, hour, minute int) time.Time {
	y, m, d := day.Date()
	// The wall-clock reading as if loc were UTC; subtracting an offset from it
	// gives the instant that reading has under that offset.
	wall := time.Date(y, m, d, hour, minute, 0, 0, time.UTC)

	// A zone changes its offset at most once within a day of any date we meet,
	// so the offsets one day either side bracket every reading on this date.
	before := offsetAt(loc, wall.Add(-24*time.Hour))
	after := offsetAt(loc, wall.Add(24*time.Hour))
	if before == after {
		return wall.Add(-time.Duration(before) * time.Second)
	}

	// A candidate instant is real only if loc is at that offset at that instant.
	atBefore := wall.Add(-time.Duration(before) * time.Second)
	atAfter := wall.Add(-time.Duration(after) * time.Second)
	okBefore := offsetAt(loc, atBefore) == before
	okAfter := offsetAt(loc, atAfter) == after
	switch {
	case okBefore && okAfter:
		// The clock read this time twice. The first occurrence is the earlier
		// instant, the one under the larger offset.
		if atBefore.Before(atAfter) {
			return atBefore
		}
		return atAfter
	case okBefore:
		return atBefore
	case okAfter:
		return atAfter
	default:
		// The clock skipped this time. Read it with the offset before the change.
		return atBefore
	}
}

func offsetAt(loc *time.Location, t time.Time) int {
	_, off := t.In(loc).Zone()
	return off
}

// wallClock is the reading of loc's clock at t, carried in a UTC time so that
// two readings can be subtracted or compared without any zone getting in.
func wallClock(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// cutoffClock is the cutoff of the given date as a wall-clock reading.
func cutoffClock(day time.Time, hour, minute int) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, hour, minute, 0, 0, time.UTC)
}

// ClosedForDay is the window that the cutoff on quoteDay closes: from the
// previous day's cutoff, inclusive, to this day's, exclusive, both in UTC.
func ClosedForDay(quoteDay time.Time, loc *time.Location, hour, minute int) domain.Window {
	day := time.Date(quoteDay.Year(), quoteDay.Month(), quoteDay.Day(), 0, 0, 0, 0, time.UTC)
	return domain.Window{
		QuoteDay: day,
		Start:    At(day.AddDate(0, 0, -1), loc, hour, minute).UTC(),
		End:      At(day, loc, hour, minute).UTC(),
	}
}

// QuoteDayOf is the quote day a moment belongs to: its own date once the
// cutoff has been reached on the wall clock (the cutoff itself already starts
// the new day), the day before otherwise.
func QuoteDayOf(t time.Time, loc *time.Location, hour, minute int) time.Time {
	local := wallClock(t, loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if local.Before(cutoffClock(day, hour, minute)) {
		return day.AddDate(0, 0, -1)
	}
	return day
}

// LastClosed is the latest window whose cutoff has passed at now. today reports
// whether that cutoff is today's; false means today's has not come yet and the
// window is yesterday's.
func LastClosed(now time.Time, loc *time.Location, hour, minute int) (w domain.Window, today bool) {
	day := QuoteDayOf(now, loc, hour, minute)
	return ClosedForDay(day, loc, hour, minute), day.Equal(Date(now, loc))
}

// Due returns the window to process at now, if a cutoff passed less than
// CatchUp ago. Today's cutoff is tried before yesterday's, which only matters
// to a cutoff shortly before midnight.
func Due(now time.Time, loc *time.Location, hour, minute int) (domain.Window, bool) {
	local := wallClock(now, loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for _, day := range []time.Time{today, today.AddDate(0, 0, -1)} {
		if delay := local.Sub(cutoffClock(day, hour, minute)); delay >= 0 && delay < CatchUp {
			return ClosedForDay(day, loc, hour, minute), true
		}
	}
	return domain.Window{}, false
}
