package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RunDateEnv names the variable that sets the date run_days is compared
// with, as YYYY-MM-DD; the orchestrator sets it once per run so that the
// agent configuration and the tests agree. Unset, it is today in UTC.
const RunDateEnv = "INFRALIB_RUN_DATE"

var weekdayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6,
}

// RunDate is the date of this run: RunDateEnv, else today in UTC.
func RunDate() (time.Time, error) {
	raw := strings.TrimSpace(os.Getenv(RunDateEnv))
	if raw == "" {
		return time.Now().UTC(), nil
	}
	date, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s=%q is not a YYYY-MM-DD date", RunDateEnv, raw)
	}
	return date, nil
}

// parseRunDays reads the day-of-week field of a cron expression: "*", a day
// (tue, tuesday, 2; 0 and 7 are both Sunday), a range (mon-fri, 1-5) or a
// comma separated list of those. "never" is no day at all. The result says,
// per time.Weekday, whether the day is included.
func parseRunDays(pattern string) ([7]bool, error) {
	var days [7]bool
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "never" {
		return days, nil
	}
	if pattern == "" {
		return days, fmt.Errorf("run_days is empty")
	}
	day := func(s string) (int, error) {
		if n, ok := weekdayNames[s]; ok {
			return n, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 7 {
			return 0, fmt.Errorf("run_days: %q is not a day of the week (sun-sat or 0-7)", s)
		}
		return n % 7, nil
	}
	for _, part := range strings.Split(pattern, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			for i := range days {
				days[i] = true
			}
			continue
		}
		from, to, isRange := strings.Cut(part, "-")
		first, err := day(strings.TrimSpace(from))
		if err != nil {
			return days, err
		}
		last := first
		if isRange {
			if last, err = day(strings.TrimSpace(to)); err != nil {
				return days, err
			}
			// 5-7 and fri-sun end on Sunday, which is day 0.
			if last < first {
				last += 7
			}
		}
		for i := first; i <= last; i++ {
			days[i%7] = true
		}
	}
	return days, nil
}

// RunsOn reports whether a whole run on the given date includes the module:
// always without run_days, else on the days it names.
func (m ModuleMeta) RunsOn(date time.Time) bool {
	if m.RunDays == "" {
		return true
	}
	days, err := parseRunDays(m.RunDays)
	if err != nil {
		return true // rejected when module.yaml was read
	}
	return days[int(date.Weekday())]
}
