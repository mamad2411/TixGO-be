package apitest

import (
	"strings"
	"testing"
)

// Body JSON di atas batas global ditolak 413 sebelum diproses; body normal tetap lolos.
func TestOversizedJSONBodyRejectedWith413(t *testing.T) {
	e := NewEnv(t)
	a := e.Anon()
	big := M{"email": "x@example.com", "password": strings.Repeat("a", 2<<20)}
	a.POST(api+"/auth/login", big).ExpectErr(413, "PAYLOAD_TOO_LARGE")
	// body wajar tetap diproses normal (kredensial salah, bukan 413)
	a.POST(api+"/auth/login", M{"email": "x@example.com", "password": "salah-banget-123"}).ExpectErr(401, "INVALID_CREDENTIALS")
}
