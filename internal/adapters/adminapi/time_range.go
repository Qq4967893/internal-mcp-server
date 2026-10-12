package adminapi

import (
	"fmt"
	"net/url"
	"time"
)

type TimeRangeQuery struct {
	StartTime string
	EndTime   string
}

func (q TimeRangeQuery) Validate() error {
	startTime, err := parseDateTime(q.StartTime, "start_time")
	if err != nil {
		return err
	}
	endTime, err := parseDateTime(q.EndTime, "end_time")
	if err != nil {
		return err
	}
	if startTime.After(endTime) {
		return invalidArgument("start_time must not be after end_time")
	}
	return nil
}

func (q TimeRangeQuery) Values() url.Values {
	return url.Values{
		"start_time": {q.StartTime},
		"end_time":   {q.EndTime},
	}
}

func parseDateTime(value, field string) (time.Time, error) {
	layouts := []string{time.DateOnly, time.DateTime}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, invalidArgument(fmt.Sprintf("%s must use YYYY-MM-DD or YYYY-MM-DD HH:MM:SS", field))
}
