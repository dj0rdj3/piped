package jobs

import (
	"context"
	"fmt"
	"time"

	"piped/internal/cron"
	"piped/internal/store"
)

type SchedulePreview struct {
	Occurrences    []time.Time `json:"occurrences"`
	ServerTimezone string      `json:"server_timezone"`
}

func (s *Service) PreviewSchedule(ctx context.Context, actor store.Actor, expression string) (SchedulePreview, error) {
	if err := s.store.Authorize(ctx, actor, store.ScopeJobsRead); err != nil {
		return SchedulePreview{}, err
	}
	schedule, err := cron.Parse(expression)
	if err != nil {
		return SchedulePreview{}, invalid(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	occurrences, err := schedule.Next(ctx, time.Now().In(time.Local), 3)
	if err != nil {
		return SchedulePreview{}, invalid("could not calculate schedule preview; try a more frequent schedule")
	}
	return SchedulePreview{Occurrences: occurrences, ServerTimezone: ServerTimezone()}, nil
}

func ServerTimezone() string {
	now := time.Now().In(time.Local)
	abbreviation, offset := now.Zone()
	name := time.Local.String()
	if name == "Local" {
		name = abbreviation
	}
	if name == "UTC" && offset == 0 {
		return "UTC"
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s (UTC%s%02d:%02d)", name, sign, offset/3600, offset%3600/60)
}
