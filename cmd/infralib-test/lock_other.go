//go:build !unix

package main

// withFileLock runs fn. Only the unix images run several test containers in
// one checkout, so nothing else needs the lock.
func withFileLock(path string, fn func() error) error {
	return fn()
}
