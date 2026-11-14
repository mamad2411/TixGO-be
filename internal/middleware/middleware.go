// Package middleware = middleware Gin: request-id, log, recover, CORS, auth, RBAC, MFA, rate limit, idempotency.
package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
	"tixgo/backend/internal/metrics"

	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/httpx"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/reqmeta"
	"tixgo/backend/internal/service"
)

// RequestID menetapkan X-Request-Id dan metadata request pada context.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-Id")
		if len(rid) < 8 || len(rid) > 64 {
			rid = id.New()
		}
		httpx.SetRequestID(c, rid)
		meta := reqmeta.Meta{IP: c.ClientIP(), UserAgent: c.GetHeader("User-Agent"), DeviceID: c.GetHeader("X-Device-Id"), RequestID: rid}
		c.Request = c.Request.WithContext(reqmeta.With(c.Request.Context(), meta))
		c.Next()
	}
}

// AccessLog mencatat satu baris per request (tanpa body/PII).
func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		metrics.ObserveHTTP(c.Request.Method, route, c.Writer.Status(), time.Since(start).Seconds())
		attrs := []any{"method", c.Request.Method, "path", c.FullPath(), "status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(), "request_id", httpx.RequestID(c)}
		if p := httpx.Principal(c); p != nil {
			attrs = append(attrs, "user", p.UserID)
		}
		if c.Writer.Status() >= 500 {
			log.Error("http", attrs...)
		} else {
			log.Info("http", attrs...)
		}
	}
}

// Recover mengubah panic menjadi 500 JSON standar.
func Recover(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic", "panic", r, "stack", string(debug.Stack()), "request_id", httpx.RequestID(c))
				httpx.Fail(c, apperr.Internal(nil))
			}
		}()
		c.Next()
	}
}

// Secure menambah header keamanan dasar untuk API.
func Secure() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// Batas ukuran body global. JSON/form kecil 1 MiB; multipart (unggah gambar 8 MiB, bukti 5 MiB, dokumen KYB) 12 MiB.
// Rute yang butuh lebih besar harus dibuka eksplisit, bukan default.
const (
	MaxJSONBody      = 1 << 20
	MaxMultipartBody = 12 << 20
)

// BodyLimit menolak body melebihi batas dengan 413 PAYLOAD_TOO_LARGE: lebih dulu lewat Content-Length,
// dan untuk body chunked lewat http.MaxBytesReader (pembacaan melewati batas gagal).
func BodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		limit := int64(MaxJSONBody)
		if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/") {
			limit = MaxMultipartBody
		}
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"code": "PAYLOAD_TOO_LARGE", "message": "ukuran permintaan terlalu besar", "details": gin.H{"max_bytes": limit}, "request_id": httpx.RequestID(c)}})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// CORS mengizinkan origin FE yang dikonfigurasi (CORS_ORIGINS) dengan kredensial.
func CORS(origins []string) gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, o := range origins {
		allowed[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && allowed[origin] {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Admission-Token, X-Request-Id, X-Device-Id, X-Requested-With, X-Cookie-Subject, X-Api-Key")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Expose-Headers", "X-Request-Id, Retry-After, Idempotent-Replay")
			h.Set("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// Auth memverifikasi Bearer JWT, memeriksa pencabutan sesi (Redis), lalu memuat principal.
func Auth(s *service.Services, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			if required {
				httpx.Fail(c, apperr.Unauthorized("login diperlukan"))
				return
			}
			c.Next()
			return
		}
		p, err := s.Tokens.ParseAccess(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			if required {
				code := apperr.CodeUnauthenticated
				msg := "token tidak valid"
				if err == auth.ErrTokenExpired {
					code, msg = apperr.CodeTokenExpired, "token kedaluwarsa"
				}
				httpx.Fail(c, apperr.New(http.StatusUnauthorized, code, msg))
				return
			}
			c.Next()
			return
		}
		if revoked, err := s.Auth.IsRevoked(c.Request.Context(), p.UserID, p.SessionID); err != nil {
			// Redis tidak terjangkau: pengunjung biasa & staf gate tetap dilayani (token berumur pendek; DB tetap menjaga stok/uang),
			// tetapi peran istimewa (uang/admin) DITOLAK — kita tak bisa memastikan sesinya belum dicabut.
			if privilegedRole(p.Role) {
				httpx.Fail(c, apperr.New(http.StatusServiceUnavailable, "UNAVAILABLE", "verifikasi sesi sementara tidak tersedia; coba lagi sebentar"))
				return
			}
			s.Log.Error("cek pencabutan sesi gagal; lanjut tanpa cek (peran non-istimewa)", "error", err)
		} else if revoked {
			httpx.Fail(c, apperr.Unauthorized("sesi sudah berakhir"))
			return
		}
		httpx.SetPrincipal(c, p)
		c.Next()
	}
}

// RequireRole membatasi ke peran tertentu (setelah Auth wajib).
func RequireRole(roles ...string) gin.HandlerFunc {
	set := map[string]bool{}
	for _, r := range roles {
		set[r] = true
	}
	return func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if p == nil {
			return
		}
		if !set[p.Role] {
			httpx.Fail(c, apperr.Forbidden("peran tidak memiliki akses"))
			return
		}
		c.Next()
	}
}

// RequireMFA mewajibkan sesi yang lolos MFA untuk peran sensitif (admin/finance) — docs/20 M27 AC6.
func RequireMFA() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if p == nil {
			return
		}
		if service.MFARequiredRole(p.Role) && !p.MFAPassed {
			httpx.Fail(c, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true))
			return
		}
		c.Next()
	}
}

// RateLimit membatasi per kunci (mis. IP). keyFn menghasilkan kunci dari request.
func RateLimit(s *service.Services, name string, limit int, period time.Duration, keyFn func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, ra, err := s.Limiter.Allow(c.Request.Context(), name+":"+keyFn(c), limit, period)
		if err == nil && !ok {
			httpx.Fail(c, apperr.TooMany("terlalu banyak permintaan").With("retry_after", int(ra.Seconds())+1))
			return
		}
		c.Next()
	}
}

func ByIP(c *gin.Context) string { return c.ClientIP() }
func ByUser(c *gin.Context) string {
	if p := httpx.Principal(c); p != nil {
		return p.UserID
	}
	return c.ClientIP()
}

// ---- Idempotency ----

type bodyWriter struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *bodyWriter) Write(b []byte) (int, error) { w.buf.Write(b); return w.ResponseWriter.Write(b) }
func (w *bodyWriter) WriteString(s string) (int, error) {
	w.buf.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// Idempotency menegakkan header Idempotency-Key pada POST pembuat order/bayar/refund (docs/11 §7).
// Kunci sama + body sama → respons tersimpan diputar ulang; body beda → 409 IDEMPOTENCY_CONFLICT.
func Idempotency(s *service.Services) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if p == nil {
			return
		}
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if len(key) < 8 || len(key) > 128 {
			httpx.Fail(c, apperr.Validation("header Idempotency-Key wajib (8–128 karakter)"))
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err != nil {
			httpx.Fail(c, apperr.Validation("body tidak terbaca"))
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(append([]byte(c.Request.Method+" "+c.Request.URL.Path+"\n"), body...))
		hash := hex.EncodeToString(sum[:])
		route := c.Request.Method + " " + c.FullPath()
		ctx := c.Request.Context()

		n, err := s.Q.ClaimIdempotency(ctx, sqlcgen.ClaimIdempotencyParams{Key: key, UserID: p.UserID, Route: route, RequestHash: hash})
		if err != nil {
			httpx.Fail(c, apperr.Internal(err))
			return
		}
		if n == 0 {
			row, err := s.Q.GetIdempotency(ctx, sqlcgen.GetIdempotencyParams{UserID: p.UserID, Route: route, Key: key})
			if err != nil {
				httpx.Fail(c, apperr.Internal(err))
				return
			}
			if row.RequestHash != hash {
				httpx.Fail(c, apperr.Conflict(apperr.CodeIdempotencyConflict, "Idempotency-Key sudah dipakai dengan isi permintaan berbeda"))
				return
			}
			if row.Response == nil || row.Status == nil {
				httpx.Fail(c, apperr.Conflict(apperr.CodeIdempotencyConflict, "permintaan dengan kunci ini sedang diproses"))
				return
			}
			c.Header("Idempotent-Replay", "true")
			if bytes.Equal(row.Response, []byte("null")) {
				c.Status(*row.Status)
			} else {
				c.Data(*row.Status, "application/json; charset=utf-8", row.Response)
			}
			c.Abort()
			return
		}
		bw := &bodyWriter{ResponseWriter: c.Writer}
		c.Writer = bw
		c.Next()
		status := c.Writer.Status()
		if status >= 500 {
			// Kegagalan server tidak disimpan: klien boleh mengulang dengan kunci yang sama.
			_ = s.Q.DeleteIdempotency(ctx, sqlcgen.DeleteIdempotencyParams{UserID: p.UserID, Route: route, Key: key})
			return
		}
		st := status
		resp := bw.buf.Bytes()
		if len(resp) == 0 {
			resp = []byte("null") // respons tanpa body (mis. 204)
		}
		_ = s.Q.CompleteIdempotency(ctx, sqlcgen.CompleteIdempotencyParams{UserID: p.UserID, Route: route, Key: key, Response: resp, Status: &st})
	}
}

// ---- konteks organizer ----

const orgKey = "tixgo.org"

// OrgFrom mengembalikan scope organizer yang dipasang RequireOrganizer.
func OrgFrom(c *gin.Context) service.OrgScope {
	v, _ := c.Get(orgKey)
	sc, _ := v.(service.OrgScope)
	return sc
}

// RequireOrganizer memuat keanggotaan organizer pemanggil (tenant isolation dimulai di sini) lalu
// membatasi ke peran tim tertentu. roles kosong = semua anggota.
func RequireOrganizer(s *service.Services, roles ...string) gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		p := httpx.MustPrincipal(c)
		if p == nil {
			return
		}
		sc, state, err := s.Organizer.Membership(c.Request.Context(), p.UserID)
		if err != nil {
			httpx.Fail(c, apperr.Internal(err))
			return
		}
		if state == "suspended" {
			httpx.Fail(c, apperr.ForbiddenCode(apperr.CodeBlocked, "organizer ditangguhkan"))
			return
		}
		if sc == nil {
			httpx.Fail(c, apperr.Forbidden("akun ini bukan anggota organizer"))
			return
		}
		if len(allowed) > 0 && !allowed[sc.Role] {
			httpx.Fail(c, apperr.Forbidden("peran tim tidak memiliki akses"))
			return
		}
		p.OrganizerID, p.OrganizerRole = sc.OrganizerID, sc.Role
		c.Set(orgKey, *sc)
		c.Next()
	}
}

// Maintenance menjawab 503 ramah untuk seluruh API publik saat mode maintenance aktif (docs/20 M39 AC4).
// Dikecualikan: health, webhook gateway (jangan kehilangan notifikasi bayar), status layanan, login/admin (agar operator bisa masuk & mematikannya).
func Maintenance(s *service.Services) gin.HandlerFunc {
	allow := []string{"/healthz", "/readyz", "/metrics", "/webhooks/", "/api/v1/status", "/api/v1/auth/", "/api/v1/admin/", "/api/v1/announcements"}
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		for _, a := range allow {
			if strings.HasPrefix(path, a) {
				c.Next()
				return
			}
		}
		if m := s.Ops.Maintenance(c.Request.Context()); m != nil {
			c.Header("Retry-After", "120")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "MAINTENANCE", "message": m.Message,
				"details": gin.H{"since": m.Since, "eta": m.ETA}, "request_id": httpx.RequestID(c)}})
			return
		}
		c.Next()
	}
}

func privilegedRole(role string) bool {
	switch role {
	case "admin", "finance", "support", "editor", "organizer":
		return true
	}
	return false
}
