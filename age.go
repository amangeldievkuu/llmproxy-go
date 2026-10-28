package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// maxDays keeps day counts far below where a time.Duration overflows.
const maxDays = 100_000

// age is a flag.Value for a length of time such as 90d, 36h or 7d12h: an
// optional whole number of days followed by a Go duration.
type age time.Duration

func (a age) String() string {
	d := time.Duration(a)
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
}

func (a *age) Set(s string) error {
	d, err := parseAge(s)
	if err != nil {
		return err
	}
	*a = age(d)
	return nil
}

func parseAge(s string) (time.Duration, error) {
	invalid := fmt.Errorf("invalid duration %q (use e.g. 90d, 36h or 7d12h)", s)
	var days int
	rest := s
	if before, after, ok := strings.Cut(s, "d"); ok {
		n, err := strconv.Atoi(before)
		if err != nil || n < 0 || n > maxDays {
			return 0, invalid
		}
		days, rest = n, after
	} else if s == "" {
		return 0, invalid
	}
	var d time.Duration
	if rest != "" {
		var err error
		if d, err = time.ParseDuration(rest); err != nil || d < 0 {
			return 0, invalid
		}
	}
	return time.Duration(days)*24*time.Hour + d, nil
}

// moment is a flag.Value for a point in time: a date such as 2026-09-01
// (midnight, local time) or how long ago, such as 7d or 36h.
type moment struct {
	date time.Time     // the date, if one was given
	ago  time.Duration // otherwise, how long before now
	set  bool          // whether the flag was given
}

// at returns the point in time, counting durations back from now.
func (m moment) at(now time.Time) time.Time {
	if !m.date.IsZero() {
		return m.date
	}
	return now.Add(-m.ago)
}

func (m moment) String() string {
	if !m.date.IsZero() {
		return m.date.Format(time.DateOnly)
	}
	return age(m.ago).String()
}

func (m *moment) Set(s string) error {
	if d, err := time.ParseInLocation(time.DateOnly, s, time.Local); err == nil {
		*m = moment{date: d, set: true}
		return nil
	}
	d, err := parseAge(s)
	if err != nil {
		return fmt.Errorf("invalid time %q (use a date like 2026-09-01 or a duration like 7d)", s)
	}
	*m = moment{ago: d, set: true}
	return nil
}
