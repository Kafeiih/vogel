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
