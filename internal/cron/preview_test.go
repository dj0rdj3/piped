package cron

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNextOccurrences(t *testing.T) {
	for _, fixture := range []struct {
		name, expression, after, zone string
		want                          []string
	}{
		{"exclusive minute", "*/5 * * * *", "2026-11-24T10:05:00Z", "UTC", []string{"2026-11-24T10:10:00Z", "2026-11-24T10:15:00Z", "2026-11-24T10:20:00Z"}},
		{"leap century", "0 0 29 2 *", "2096-03-01T00:00:00Z", "UTC", []string{"2104-02-29T00:00:00Z", "2108-02-29T00:00:00Z", "2112-02-29T00:00:00Z"}},
		{"spring gap", "30 2 * * *", "2026-03-28T03:00:00+01:00", "Europe/Belgrade", []string{"2026-03-30T02:30:00+02:00", "2026-03-31T02:30:00+02:00", "2026-04-01T02:30:00+02:00"}},
		{"fall repeat", "30 2 * * *", "2026-10-25T00:00:00+02:00", "Europe/Belgrade", []string{"2026-10-25T02:30:00+02:00", "2026-10-25T02:30:00+01:00", "2026-10-26T02:30:00+01:00"}},
		{"half hour transition", "45 1 * * *", "2026-04-05T00:00:00+11:00", "Australia/Lord_Howe", []string{"2026-04-05T01:45:00+11:00", "2026-04-05T01:45:00+10:30", "2026-04-06T01:45:00+10:30"}},
		{"impossible date", "0 0 30 2 *", "2026-01-01T00:00:00Z", "UTC", []string{}},
		{"impossible date in system timezone", "0 0 30 2 *", "2026-01-01T00:00:00Z", "Local", []string{}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			schedule, err := Parse(fixture.expression)
			if err != nil {
				t.Fatal(err)
			}
			location, err := time.LoadLocation(fixture.zone)
			if err != nil {
				t.Fatal(err)
			}
			after, err := time.Parse(time.RFC3339, fixture.after)
			if err != nil {
				t.Fatal(err)
			}
			result, err := schedule.Next(context.Background(), after.In(location), 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(result) != len(fixture.want) {
				t.Fatalf("got %v, want %v", result, fixture.want)
			}
			for index, at := range result {
				if got := at.Format(time.RFC3339); got != fixture.want[index] {
					t.Fatalf("got %s, want %s", got, fixture.want[index])
				}
			}
		})
	}
}

func TestNextCancellationAndDaySemantics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	schedule, _ := Parse("0 0 30 2 *")
	if _, err := schedule.Next(ctx, time.Now(), 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search: %v", err)
	}
	for _, expression := range []string{"10,30 2 20-25 * MON", "*/15 * * * MON-FRI", "0 3 * * *"} {
		schedule, _ := Parse(expression)
		location, _ := time.LoadLocation("Europe/Belgrade")
		after := time.Date(2026, 10, 24, 23, 0, 0, 0, location)
		result, err := schedule.Next(context.Background(), after, 3)
		if err != nil {
			t.Fatal(err)
		}
		var want []time.Time
		for at := after.Add(time.Minute); len(want) < 3; at = at.Add(time.Minute) {
			if schedule.Matches(at) {
				want = append(want, at)
			}
		}
		for index, at := range result {
			if !at.Equal(want[index]) {
				t.Fatalf("%s: got %v, want %v", expression, result, want)
			}
		}
	}
}

func TestNextMatchesMinuteWalkAcrossCalendarAndTimezoneBoundaries(t *testing.T) {
	for _, zone := range []string{"Europe/Belgrade", "America/St_Johns", "Australia/Lord_Howe", "Pacific/Chatham", "Asia/Kathmandu"} {
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, date := range []string{"2026-03-28T23:58:27Z", "2026-04-04T12:58:00Z", "2026-10-24T22:58:00Z", "2026-12-31T23:58:00Z"} {
			after, err := time.Parse(time.RFC3339, date)
			if err != nil {
				t.Fatal(err)
			}
			after = after.In(location)
			for _, expression := range []string{"*/17 * * * *", "5,35 1-4 * * *", "0 0 * * MON,FRI", "7 0-23/3 1,15 * MON", "* */7 */2 * TUE-THU", "12 23 28-31 * *"} {
				t.Run(zone+"/"+date+"/"+expression, func(t *testing.T) {
					schedule, err := Parse(expression)
					if err != nil {
						t.Fatal(err)
					}
					const count = 6
					got, err := schedule.Next(context.Background(), after, count)
					if err != nil {
						t.Fatal(err)
					}
					want := []time.Time{}
					limit := after.AddDate(1, 0, 0)
					for at := after.Truncate(time.Minute).Add(time.Minute); at.Before(limit) && len(want) < count; at = at.Add(time.Minute) {
						if schedule.Matches(at) {
							want = append(want, at)
						}
					}
					if len(got) != count || len(want) != count {
						t.Fatalf("missing occurrences: got %v, want %v", got, want)
					}
					for index := range want {
						if !got[index].Equal(want[index]) {
							t.Fatalf("got %v, want %v", got, want)
						}
					}
				})
			}
		}
	}
}
