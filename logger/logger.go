// Package logger holds the minimal testing contract the framework needs and a
// timestamped log helper. Every helper in this module takes a logger.T rather
// than *testing.T so tests can pass a wrapped or recording T where useful.
package logger

import (
	"fmt"
	"time"
)

// T is the subset of *testing.T used by the framework. *testing.T satisfies it,
// and so does anything testify's require package accepts plus Helper/Logf/Name.
type T interface {
	Helper()
	Name() string
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	FailNow()
}

// Logf writes a timestamped line into the test log. With `go test -json` the
// line is attributed to the running test, which is what the runner relies on
// to show only a failed test's output.
func Logf(t T, format string, args ...any) {
	t.Helper()
	t.Logf("%s %s", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// Log is Logf without formatting.
func Log(t T, args ...any) {
	t.Helper()
	t.Logf("%s %s", time.Now().UTC().Format(time.RFC3339), fmt.Sprint(args...))
}
