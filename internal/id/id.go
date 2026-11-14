// Package id menghasilkan ULID 26 karakter (docs/02-CONVENTIONS: ID).
package id

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New menghasilkan ULID baru (monotonik dalam milidetik yang sama).
func New() string {
	mu.Lock()
	defer mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// Valid memeriksa bentuk ULID (panjang & karakter), bukan keberadaannya.
func Valid(s string) bool {
	if len(s) != 26 {
		return false
	}
	_, err := ulid.ParseStrict(s)
	return err == nil
}
