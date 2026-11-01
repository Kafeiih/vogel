package calendar_test

import (
	"context"
	"errors"
	"testing"
	"time"
	_ "time/tzdata" // America/Santiago without relying on the host zoneinfo

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/calendar"
)

// holidaySet is a Holidays backed by a fixed set of dates.
type holidaySet map[calendar.Date]bool

func (h holidaySet) IsHoliday(_ context.Context, d calendar.Date) (bool, error) { return h[d], nil }

type failingHolidays struct{}

func (failingHolidays) IsHoliday(context.Context, calendar.Date) (bool, error) {
	return false, errors.New("holiday table missing")
}

func TestCalendar_BusinessDaysSkipsWeekendsAndHolidays(t *testing.T) {
	ctx := context.Background()
	friday := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	holidays := holidaySet{{Year: 2026, Month: time.November, Day: 2}: true} // Monday

	due, err := calendar.BusinessDays(2, time.UTC, holidays)(ctx, friday)
	require.NoError(t, err)
	// Sat, Sun and the Monday holiday are skipped: Tue 3 and Wed 4 are the
	// two business days, so the deadline is the end of Wednesday.
	assert.Equal(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), due)

	due, err = calendar.BusinessDays(1, time.UTC, nil)(ctx, friday)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), due, "nil Holidays skips weekends only")

	_, err = calendar.BusinessDays(1, time.UTC, failingHolidays{})(ctx, friday)
	require.ErrorContains(t, err, "holiday table missing")

	_, err = calendar.BusinessDays(0, time.UTC, nil)(ctx, friday)
	require.Error(t, err, "n must be at least 1")
	_, err = calendar.BusinessDays(1, nil, nil)(ctx, friday)
	require.Error(t, err, "the location is explicit")
}

func TestCalendar_EvaluatesInExplicitLocation(t *testing.T) {
	ctx := context.Background()
	santiago, err := time.LoadLocation("America/Santiago")
	require.NoError(t, err)
	// Thursday 23:30 in Santiago (UTC-3 in October 2026) is already Friday
	// 02:30 in UTC.
	from := time.Date(2026, 10, 29, 23, 30, 0, 0, santiago)
	require.Equal(t, time.Friday, from.UTC().Weekday())

	due, err := calendar.BusinessDays(1, santiago, nil)(ctx, from)
	require.NoError(t, err)
	assert.True(t, time.Date(2026, 10, 31, 0, 0, 0, 0, santiago).Equal(due),
		"in Santiago the next business day is Friday: due at the end of it, got %s", due)
	assert.Equal(t, santiago, due.Location())

	due, err = calendar.BusinessDays(1, time.UTC, nil)(ctx, from)
	require.NoError(t, err)
	assert.True(t, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC).Equal(due),
		"in UTC the same instant is Friday, so the next business day is Monday, got %s", due)
}

func TestRules_ResolvesByName(t *testing.T) {
	rules := calendar.Rules{"two-business-days": calendar.BusinessDays(2, time.UTC, nil)}
	friday := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

	due, err := rules.Due(context.Background(), workflow.DueInput{Rule: "two-business-days", From: friday})
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), due)

	_, err = rules.Due(context.Background(), workflow.DueInput{Rule: "nope", From: friday})
	require.ErrorIs(t, err, workflow.ErrUnknownDeadlineRule)
}

// everyDay reports every date as a holiday: a broken calendar.
type everyDay struct{}

func (everyDay) IsHoliday(context.Context, calendar.Date) (bool, error) { return true, nil }

// holidayRanges reports every day inside one of its inclusive ranges as a
// holiday.
type holidayRanges [][2]calendar.Date

func (h holidayRanges) IsHoliday(_ context.Context, d calendar.Date) (bool, error) {
	at := func(d calendar.Date) time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }
	for _, r := range h {
		if !at(d).Before(at(r[0])) && !at(d).After(at(r[1])) {
			return true, nil
		}
	}
	return false, nil
}

func TestCalendar_BusinessDaysIsBounded(t *testing.T) {
	friday := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	_, err := calendar.BusinessDays(1, time.UTC, everyDay{})(context.Background(), friday)
	require.EqualError(t, err, "calendar: business days: no business day in the 366 days after 2026-10-30",
		"a calendar without business days fails instead of looping forever, naming where the gap starts")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = calendar.BusinessDays(1, time.UTC, holidaySet{})(ctx, friday)
	require.ErrorIs(t, err, context.Canceled)
}

func TestCalendar_BusinessDaysGapLimitIs366Days(t *testing.T) {
	wednesday := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

	// 365 non-business days, then Friday 2027-10-29: still within the limit.
	h := holidayRanges{{{Year: 2026, Month: 10, Day: 29}, {Year: 2027, Month: 10, Day: 28}}}
	due, err := calendar.BusinessDays(1, time.UTC, h)(context.Background(), wednesday)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2027, 10, 30, 0, 0, 0, 0, time.UTC), due)

	// 366 non-business days: past the limit.
	h = holidayRanges{{{Year: 2026, Month: 10, Day: 29}, {Year: 2027, Month: 10, Day: 29}}}
	_, err = calendar.BusinessDays(1, time.UTC, h)(context.Background(), wednesday)
	require.EqualError(t, err, "calendar: business days: no business day in the 366 days after 2026-10-28")
}

func TestCalendar_BusinessDaysGapRestartsAfterEachBusinessDay(t *testing.T) {
	wednesday := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	// 365 non-business days, Friday 2027-10-29, 11 more, then Tuesday
	// 2027-11-09: no single gap reaches the limit, though both together do.
	h := holidayRanges{
		{{Year: 2026, Month: 10, Day: 29}, {Year: 2027, Month: 10, Day: 28}},
		{{Year: 2027, Month: 10, Day: 30}, {Year: 2027, Month: 11, Day: 8}},
	}
	due, err := calendar.BusinessDays(2, time.UTC, h)(context.Background(), wednesday)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2027, 11, 10, 0, 0, 0, 0, time.UTC), due)
}
