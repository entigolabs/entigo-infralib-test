// Package random provides short unique identifiers for test resource names.
package random

import (
	"crypto/rand"
	"math/big"
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// UniqueId returns a 6 character base62 string, the same shape terratest's
// random.UniqueId produced, so existing resource name length assumptions hold.
func UniqueId() string {
	return String(6, base62)
}

// LowerId returns an n character lowercase alphanumeric string suitable for
// Kubernetes names.
func LowerId(n int) string {
	return String(n, "abcdefghijklmnopqrstuvwxyz0123456789")
}

// String returns n characters drawn from alphabet using crypto/rand.
func String(n int, alphabet string) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}
