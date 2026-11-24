// Package cron parses standard five-field, minute-resolution cron expressions.
// Scheduling policy and timezone selection belong to the execution engine.
package cron

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrSyntax = errors.New("use five cron fields: minute hour day-of-month month weekday; valid numbers, names, *, lists, ranges, and range/* steps are supported")

type field struct {
	values   uint64
	wildcard bool
}

func (f field) matches(value int) bool {
	return f.values&(uint64(1)<<value) != 0
}

type Schedule struct{ fields [5]field }

var monthNames = []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}
var weekdayNames = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

func Parse(expression string) (Schedule, error) {
	var schedule Schedule
	if len(expression) == 0 || len(expression) > 256 {
		return schedule, ErrSyntax
	}
	for _, letter := range expression {
		if letter > 127 || letter < ' ' && letter != '\t' {
			return schedule, ErrSyntax
		}
	}
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return schedule, ErrSyntax
	}
	limits := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, part := range parts {
		parsed, err := parseField(strings.ToUpper(part), limits[index][0], limits[index][1], index)
		if err != nil {
			return Schedule{}, ErrSyntax
		}
		schedule.fields[index] = parsed
	}
	return schedule, nil
}

func parseField(text string, minimum, maximum, index int) (field, error) {
	parsed := field{wildcard: strings.Contains(text, "*")}
	for _, item := range strings.Split(text, ",") {
		step := 1
		base, stepText, stepped := strings.Cut(item, "/")
		if stepped {
			var err error
			step, err = number(stepText)
			if err != nil || step < 1 {
				return field{}, ErrSyntax
			}
		}
		start, end := minimum, maximum
		if base != "*" {
			first, last, ranged := strings.Cut(base, "-")
			var err error
			start, err = fieldNumber(first, index)
			if err != nil {
				return field{}, err
			}
			end = start
			if ranged {
				end, err = fieldNumber(last, index)
				if err != nil {
					return field{}, err
				}
			} else if stepped {
				return field{}, ErrSyntax
			}
		}
		if start < minimum || end > maximum || start > end {
			return field{}, ErrSyntax
		}
		// Iteration is bounded by the field range even for very large steps.
		for value := start; value <= end; value++ {
			if (value-start)%step == 0 {
				parsed.values |= uint64(1) << value
			}
		}
	}
	if index == 4 && parsed.values&(uint64(1)<<7) != 0 {
		parsed.values |= 1
	}
	if parsed.values == 0 {
		return field{}, ErrSyntax
	}
	return parsed, nil
}

func number(text string) (int, error) {
	if text == "" {
		return 0, ErrSyntax
	}
	for _, letter := range text {
		if letter < '0' || letter > '9' {
			return 0, ErrSyntax
		}
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, ErrSyntax
	}
	return value, nil
}

func fieldNumber(text string, index int) (int, error) {
	names := []string{}
	offset := 0
	if index == 3 {
		names = monthNames
		offset = 1
	}
	if index == 4 {
		names = weekdayNames
	}
	for value, name := range names {
		if text == name {
			return value + offset, nil
		}
	}
	return number(text)
}

// Matches evaluates the supplied local calendar time. When both day fields
// are restricted, either may match; a wildcard day field uses the other one.
func (s Schedule) Matches(at time.Time) bool {
	return s.fields[0].matches(at.Minute()) && s.fields[1].matches(at.Hour()) &&
		s.fields[3].matches(int(at.Month())) && s.matchesDay(at)
}

// Next returns up to count future matches in after's location. Sixteen years
// covers three leap-day occurrences even across a non-leap century. Impossible
// calendar combinations return an empty slice. Skips never cross timezone
// transitions, preserving missing and repeated local minutes.
func (s Schedule) Next(ctx context.Context, after time.Time, count int) ([]time.Time, error) {
	occurrences := []time.Time{}
	limit := after.AddDate(16, 0, 0)
	for at := after.Truncate(time.Minute).Add(time.Minute); !at.After(limit) && len(occurrences) < count; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Minutes cannot match when this hour, date, or month is excluded.
		if !s.fields[1].matches(at.Hour()) || !s.fields[3].matches(int(at.Month())) || !s.matchesDay(at) {
			next := at.Add(time.Duration(60-at.Minute()) * time.Minute)
			_, zoneEnd := at.ZoneBounds()
			if zoneEnd.After(at) && zoneEnd.Before(next) {
				next = zoneEnd
			}
			at = next
		} else {
			if s.fields[0].matches(at.Minute()) {
				occurrences = append(occurrences, at)
			}
			at = at.Add(time.Minute)
		}
	}
	return occurrences, nil
}

func (s Schedule) matchesDay(at time.Time) bool {
	day := s.fields[2].matches(at.Day())
	weekday := s.fields[4].matches(int(at.Weekday()))
	if s.fields[2].wildcard || s.fields[4].wildcard {
		return day && weekday
	}
	return day || weekday
}
