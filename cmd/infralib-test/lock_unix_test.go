//go:build unix

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWithFileLockSerialises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "align.lock")
	holding := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- withFileLock(path, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	second := make(chan error, 1)
	go func() {
		second <- withFileLock(path, func() error { return nil })
	}()
	select {
	case err := <-second:
		t.Fatalf("second lock ran while the first was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
}
