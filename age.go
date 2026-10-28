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
