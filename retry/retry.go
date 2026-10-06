// Package retry re-implements the small part of terratest's retry module the
// module tests use: run an action until it stops returning an error.
package retry

import (
	"errors"
	"fmt"
	"time"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

// MaxRetriesExceeded is returned when the action never succeeded. LastError is
// the error of the final attempt.
type MaxRetriesExceeded struct {
	Description string
	MaxRetries  int
	LastError   error
}

func (err MaxRetriesExceeded) Error() string {
	return fmt.Sprintf("'%s' unsuccessful after %d retries, last error: %v", err.Description, err.MaxRetries, err.LastError)
}

func (err MaxRetriesExceeded) Unwrap() error { return err.LastError }

// DoWithRetryE runs action up to maxRetries times, sleeping sleepBetweenRetries
// between attempts, and returns the first successful result. The action's
// error of each failed attempt is logged.
func DoWithRetryE(t logger.T, actionDescription string, maxRetries int, sleepBetweenRetries time.Duration, action func() (string, error)) (string, error) {
	t.Helper()
	var lastErr error
	for i := 0; i <= maxRetries; i++ {
		logger.Logf(t, "%s", actionDescription)
		output, err := action()
		if err == nil {
			return output, nil
		}
		lastErr = err
		var fatal FatalError
		if errors.As(err, &fatal) {
			logger.Logf(t, "%s returned a fatal error: %v", actionDescription, err)
			return "", fatal.Underlying
		}
		if i < maxRetries {
			logger.Logf(t, "%s returned an error: %v. Sleeping for %s and will try again.", actionDescription, err, sleepBetweenRetries)
			time.Sleep(sleepBetweenRetries)
		}
	}
	return "", MaxRetriesExceeded{Description: actionDescription, MaxRetries: maxRetries, LastError: lastErr}
}

// DoWithRetry is DoWithRetryE that fails the test instead of returning an error.
func DoWithRetry(t logger.T, actionDescription string, maxRetries int, sleepBetweenRetries time.Duration, action func() (string, error)) string {
	t.Helper()
	output, err := DoWithRetryE(t, actionDescription, maxRetries, sleepBetweenRetries, action)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return output
}

// FatalError wraps an error the action knows will not recover; DoWithRetryE
// stops retrying and returns it immediately.
type FatalError struct{ Underlying error }

func (err FatalError) Error() string { return err.Underlying.Error() }
func (err FatalError) Unwrap() error { return err.Underlying }
