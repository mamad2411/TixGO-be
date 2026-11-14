//go:build tools

// Package platform — pin dependensi yang sudah DIPILIH & DIVERIFIKASI (docs/21-TOOLING.md)
// tetapi belum dipakai kode. Hapus baris import di sini saat modul pemakainya mengimpor paketnya.
// Tag `tools` mencegah ikut ter-build; `go mod tidy` tetap menyimpannya di go.mod.
package platform

import (
	_ "github.com/go-pdf/fpdf"
	_ "github.com/go-redis/redis_rate/v10"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/hibiken/asynq"
	_ "github.com/microcosm-cc/bluemonday"
	_ "github.com/midtrans/midtrans-go/snap"
	_ "github.com/minio/minio-go/v7"
	_ "github.com/oklog/ulid/v2"
	_ "github.com/olahol/melody"
	_ "github.com/pquerna/otp/totp"
	_ "github.com/stretchr/testify/require"
	_ "github.com/wneessen/go-mail"
	_ "github.com/xuri/excelize/v2"
	_ "github.com/yuin/goldmark"
	_ "golang.org/x/crypto/argon2"
)
