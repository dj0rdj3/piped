package cron

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFiveFieldMatching(t *testing.T) {
	cases := []struct {
		expression, at string
		want           bool
	}{
		{"*/15 9-17 * JAN-MAR MON-FRI", "2026-02-23T10:30:00Z", true},
		{"*/15 9-17 * JAN-MAR MON-FRI", "2026-02-22T10:30:00Z", false},
		{"*/15 9-17 * JAN-MAR MON-FRI", "2026-02-23T10:31:00Z", false},
		{"0,30 0-23/2 * * *", "2026-11-23T02:30:00Z", true},
		{"0,30 0-23/2 * * *", "2026-11-23T03:30:00Z", false},
		{"0 0 1 * MON", "2026-11-23T00:00:00Z", true},
		{"0 0 1 * MON", "2026-12-01T00:00:00Z", true},
		{"0 0 1 * MON", "2026-11-24T00:00:00Z", false},
		{"0 0 * * MON", "2026-12-01T00:00:00Z", false},
		{"0 0 */2 * MON", "2026-11-23T00:00:00Z", true},
		{"0 0 */2 * MON", "2026-11-30T00:00:00Z", false},
		{"0 0 * * 7", "2026-11-22T00:00:00Z", true},
		{"0 0 * * sun", "2026-11-22T00:00:00Z", true},
		{"0 0 29 FEB *", "2024-02-29T00:00:00Z", true},
		{"0 0 29 FEB *", "2025-03-01T00:00:00Z", false},
		{"0 0 * JAN-MAR/2 *", "2026-02-01T00:00:00Z", false},
		{"*/1000 * * * *", "2026-11-23T00:00:00Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.expression+tc.at, func(t *testing.T) {
			schedule, err := Parse(tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if schedule.Matches(at) != tc.want {
				t.Fatalf("%s at %s: expected %v", tc.expression, tc.at, tc.want)
			}
		})
	}
}

func TestCronRejectsInvalidAndUnsupportedSyntax(t *testing.T) {
	for _, expression := range []string{"", "* * * *", "* * * * * *", "@daily", "TZ=UTC * * * * *", "CRON_TZ=UTC * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "-1 * * * *", "0-60 * * * *", "5-1 * * * *", "*/0 * * * *", "1/2 * * * *", "1,,2 * * * *", "*/-1 * * * *", "*/99999999999999999999999 * * * *", "* * * nope *", "* * * * MON-SUN", "* * * * *\n", "\u00a0* * * * *", strings.Repeat("*", 257)} {
		if _, err := Parse(expression); !errors.Is(err, ErrSyntax) {
			t.Fatalf("accepted invalid expression %q", expression)
		}
	}
}

func TestCronUsesTheSuppliedServerCalendarAndDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := Parse("30 1 * * *")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"} {
		at, _ := time.Parse(time.RFC3339, text)
		if !schedule.Matches(at.In(location)) || schedule.Matches(at) {
			t.Fatal("server-local repeated hour not matched independently of UTC")
		}
	}
	missing, _ := Parse("30 2 * * *")
	start, _ := time.Parse(time.RFC3339, "2026-03-08T06:00:00Z")
	for minutes := 0; minutes < 180; minutes++ {
		if missing.Matches(start.Add(time.Duration(minutes) * time.Minute).In(location)) {
			t.Fatal("nonexistent local time matched")
		}
	}
}
