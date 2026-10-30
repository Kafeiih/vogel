// Package calendar computes business-calendar deadlines for vogel/workflow:
// "N business days" counted in an explicit time zone, skipping weekends and
// the holidays a consumer supplies. Holiday data is the consumer's, never
// this package's.
//
// Wire it through workflow.Config.Deadlines with Rules:
//
//	cfg := workflow.Config{Deadlines: calendar.Rules{
//		"plazo-5-dias-habiles": calendar.BusinessDays(5, santiago, holidays),
//	}}
//
// and name the rule on a node (workflow.Node.DeadlineRule).
package calendar

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kafeiih/vogel/workflow"
)

// Date is a civil date, without time of day or time zone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the civil date of t in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{Year: y, Month: m, Day: d}
}

// Holidays reports whether a civil date is a non-business day besides
// weekends. Implementations run inside the caller's transaction (see
// workflow.DeadlineResolver), so they must not call the network: keep the
// calendar in memory or read it from the database through the context.
type Holidays interface {
	IsHoliday(ctx context.Context, d Date) (bool, error)
}

// maxGapDays bounds how many consecutive non-business days BusinessDays
// skips before it gives up: a Holidays that reports every day as a holiday
// would otherwise loop forever inside the caller's transaction.
const maxGapDays = 366

// DueFunc returns the deadline of something that starts at from.
type DueFunc func(ctx context.Context, from time.Time) (time.Time, error)

// BusinessDays returns a DueFunc whose deadline is the end of the n-th
// business day after from's civil date, both evaluated in loc: the instant
// the following day starts (00:00 in loc), returned in loc. A business day is
// a Monday to Friday that h does not report as a holiday; a nil h skips
// weekends only. The day of from itself never counts.
//
// loc is required, so the day boundaries never depend on the server's time
// zone or on from's location. The DueFunc fails when n < 1, when loc is nil,
// when h fails, when ctx is done, or when it finds no business day within a
// year of the previous one (a misconfigured Holidays).
func BusinessDays(n int, loc *time.Location, h Holidays) DueFunc {
	return func(ctx context.Context, from time.Time) (time.Time, error) {
		if n < 1 {
			return time.Time{}, fmt.Errorf("calendar: business days: n must be at least 1, got %d", n)
		}
		if loc == nil {
			return time.Time{}, errors.New("calendar: business days: location is required")
		}
		day := DateOf(from.In(loc))
		for counted, gap := 0, 0; counted < n; {
			if err := ctx.Err(); err != nil {
				return time.Time{}, fmt.Errorf("calendar: business days: %w", err)
			}
			day = day.addDays(1)
			ok, err := isBusinessDay(ctx, day, h)
			if err != nil {
				return time.Time{}, err
			}
			if !ok {
				gap++
				if gap > maxGapDays {
					return time.Time{}, fmt.Errorf("calendar: business days: no business day in the %d days after %04d-%02d-%02d", maxGapDays, day.Year, day.Month, day.Day)
				}
				continue
			}
			counted, gap = counted+1, 0
		}
		end := day.addDays(1)
		return time.Date(end.Year, end.Month, end.Day, 0, 0, 0, 0, loc), nil
	}
}

// isBusinessDay reports whether d is a weekday h does not report as a
// holiday.
func isBusinessDay(ctx context.Context, d Date, h Holidays) (bool, error) {
	if wd := d.weekday(); wd == time.Saturday || wd == time.Sunday {
		return false, nil
	}
	if h == nil {
		return true, nil
	}
	holiday, err := h.IsHoliday(ctx, d)
	if err != nil {
		return false, fmt.Errorf("calendar: holiday %04d-%02d-%02d: %w", d.Year, d.Month, d.Day, err)
	}
	return !holiday, nil
}

// addDays returns the civil date n days after d.
func (d Date) addDays(n int) Date {
	return DateOf(time.Date(d.Year, d.Month, d.Day+n, 12, 0, 0, 0, time.UTC))
}

func (d Date) weekday() time.Weekday {
	return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC).Weekday()
}

// Rules is a workflow.DeadlineResolver that maps each Node.DeadlineRule name
// to its DueFunc, evaluated from the time the token entered the node.
type Rules map[string]DueFunc

var _ workflow.DeadlineResolver = Rules(nil)

// Due resolves in.Rule. A name with no DueFunc fails with
// workflow.ErrUnknownDeadlineRule.
//
//nolint:gocritic // hugeParam: DeadlineResolver takes DueInput by value, so a resolver cannot change the engine's case or token.
func (r Rules) Due(ctx context.Context, in workflow.DueInput) (time.Time, error) {
	fn, ok := r[in.Rule]
	if !ok || fn == nil {
		return time.Time{}, fmt.Errorf("calendar: %w: %q", workflow.ErrUnknownDeadlineRule, in.Rule)
	}
	return fn(ctx, in.From)
}
