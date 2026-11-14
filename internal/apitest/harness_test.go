// Package apitest = tes integrasi seluruh API: Postgres & Redis nyata (infra dev), gateway/email/SMS/storage palsu,
// jam terkendali. Seluruh tes ada dalam SATU paket agar berjalan berurutan pada DB yang sama.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/clock"
	"tixgo/backend/internal/config"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/payment"
	"tixgo/backend/internal/ratelimit"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/server"
	"tixgo/backend/internal/service"
	"tixgo/backend/internal/storage"
)

const (
	testPassword = "Correct-Horse-9!"
	testEncKey   = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
)

var (
	adminDSN string
	testDSN  string
	redisAdr string
	pool     *pgxpool.Pool
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	adminDSN = env("TEST_ADMIN_DATABASE_URL", "postgres://tixgo:tixgo@localhost:5433/postgres?sslmode=disable")
	testDSN = env("TEST_DATABASE_URL", "postgres://tixgo:tixgo@localhost:5433/tixgo_test?sslmode=disable")
	redisAdr = env("TEST_REDIS_ADDR", "localhost:6380")
	if os.Getenv("TIXGO_SKIP_INTEGRATION") == "1" {
		fmt.Println("TIXGO_SKIP_INTEGRATION=1: tes integrasi dilewati")
		os.Exit(0)
	}
	ctx := context.Background()
	if err := prepareDB(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "GAGAL menyiapkan DB uji (jalankan `make infra`):", err)
		os.Exit(1)
	}
	var err error
	pool, err = pgxpool.New(ctx, testDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		os.Exit(1)
	}
	code := m.Run()
	pool.Close()
	os.Exit(code)
}

func prepareDB(ctx context.Context) error {
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		return err
	}
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='tixgo_test')").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := admin.Exec(ctx, "CREATE DATABASE tixgo_test"); err != nil {
			return err
		}
	}
	p, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		return err
	}
	defer p.Close()
	if _, err := p.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		return err
	}
	files, _ := filepath.Glob("../../migrations/*.up.sql")
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("migrasi tidak ditemukan")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := p.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

// Env = satu lingkungan uji lengkap.
type Env struct {
	T       *testing.T
	Svc     *service.Services
	Engine  *gin.Engine
	Clock   *clock.Fake
	Mail    *notify.Memory
	Gateway *payment.Fake
	Store   *storage.Memory
	Jobs    *jobs.Inline
	Redis   *redis.Client
	Cfg     *config.Config
	ctx     context.Context
}

func testConfig() *config.Config {
	cfgEnv := map[string]string{
		"APP_ENV": "test", "DATABASE_URL": testDSN, "REDIS_ADDR": redisAdr, "REDIS_DB": "15",
		"CORS_ORIGINS": "http://localhost:5174", "JWT_SECRET": strings.Repeat("s", 40), "ENC_KEY": testEncKey,
		"S3_ENDPOINT": "x", "S3_BUCKET": "x", "S3_ACCESS_KEY": "x", "S3_SECRET_KEY": "x",
		"MIDTRANS_SERVER_KEY": payment.FakeServerKey, "ARGON_MEMORY_KB": "1024", "ARGON_TIME": "1", "ARGON_THREADS": "1",
		"PUBLIC_WEB_URL": "http://web.test", "PUBLIC_API_URL": "http://api.test", "WEBHOOK_ALLOW_PRIVATE": "true",
	}
	cfg, err := config.Load(func(k string) string { return cfgEnv[k] })
	if err != nil {
		panic(err)
	}
	return cfg
}

// NewEnv membuat lingkungan bersih: DB dikosongkan, Redis DB 15 di-flush.
func NewEnv(t *testing.T) *Env {
	t.Helper()
	ctx := context.Background()
	truncateAll(t, ctx)
	cfg := testConfig()
	rdb := redis.NewClient(&redis.Options{Addr: redisAdr, DB: 15})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis tidak terjangkau (make infra): %v", err)
	}
	rdb.FlushDB(ctx)
	clk := clock.NewFake(time.Now().UTC().Truncate(time.Second))
	mem := notify.NewMemory()
	store := storage.NewMemory()
	gw := payment.NewFake(clk.Now)
	reg := jobs.NewRegistry()
	inline := jobs.NewInline(reg, clk.Now)
	box, _ := auth.NewBox(cfg.EncKey)
	logOut := io.Writer(io.Discard)
	if os.Getenv("TIXGO_TEST_LOG") == "1" {
		logOut = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	d := &service.Deps{
		Cfg: cfg, Log: log, Pool: pool, Redis: rdb, Clock: clk,
		Tokens: auth.NewTokens(cfg.JWTSecret, cfg.JWTAccessTTL, clk),
		Hasher: auth.NewPasswordHasher(auth.PasswordParams{MemoryKB: 1024, Time: 1, Threads: 1}),
		Box:    box, Mailer: mem, SMS: mem.SMS(), Push: notify.NoPush{}, Gateway: gw, Store: store,
		Jobs: inline, Reg: reg, Limiter: ratelimit.New(rdb, false), Q: sqlcgen.New(pool),
	}
	svc := service.New(d)
	engine, err := server.New(svc, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return &Env{T: t, Svc: svc, Engine: engine, Clock: clk, Mail: mem, Gateway: gw, Store: store, Jobs: inline, Redis: rdb, Cfg: cfg, ctx: ctx}
}

func truncateAll(t *testing.T, ctx context.Context) {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname='public'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, `"`+n+`"`)
	}
	rows.Close()
	if len(tables) == 0 {
		return
	}
	if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ",")+" RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, "ALTER SEQUENCE ticket_entry_seq RESTART")
}

// EnableRateLimit mengaktifkan limiter nyata untuk tes yang menguji pembatasan laju.
func (e *Env) EnableRateLimit() { e.Svc.Limiter = ratelimit.New(e.Redis, true) }

// ---------- HTTP client ----------

type Client struct {
	e       *Env
	Token   string
	Cookies map[string]string
	IP      string
	Device  string
	UA      string
	Email   string
	UserID  string
}

type Resp struct {
	Code   int
	Body   []byte
	Header http.Header
	t      *testing.T
}

func (e *Env) Anon() *Client {
	return &Client{e: e, Cookies: map[string]string{}, IP: "203.0.113.10", UA: "Mozilla/5.0 test"}
}

func (c *Client) WithIP(ip string) *Client { c.IP = ip; return c }

func (c *Client) Do(method, path string, body any, headers ...string) *Resp {
	c.e.T.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		case []byte:
			rd = bytes.NewReader(b)
		default:
			j, err := json.Marshal(body)
			if err != nil {
				c.e.T.Fatal(err)
			}
			rd = bytes.NewReader(j)
		}
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = c.IP + ":1234"
	req.Header.Set("User-Agent", c.UA)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Device != "" {
		req.Header.Set("X-Device-Id", c.Device)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	for k, v := range c.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.e.Engine.ServeHTTP(w, req)
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	for _, ck := range res.Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.Cookies, ck.Name)
		} else {
			c.Cookies[ck.Name] = ck.Value
		}
	}
	return &Resp{Code: res.StatusCode, Body: b, Header: res.Header, t: c.e.T}
}

func (c *Client) GET(path string, h ...string) *Resp {
	c.e.T.Helper()
	return c.Do("GET", path, nil, h...)
}
func (c *Client) POST(path string, body any, h ...string) *Resp {
	c.e.T.Helper()
	return c.Do("POST", path, body, h...)
}
func (c *Client) PUT(path string, body any, h ...string) *Resp {
	c.e.T.Helper()
	return c.Do("PUT", path, body, h...)
}
func (c *Client) PATCH(path string, body any, h ...string) *Resp {
	c.e.T.Helper()
	return c.Do("PATCH", path, body, h...)
}
func (c *Client) DELETE(path string, h ...string) *Resp {
	c.e.T.Helper()
	return c.Do("DELETE", path, nil, h...)
}

// IdemPOST = POST dengan Idempotency-Key.
func (c *Client) IdemPOST(path, key string, body any) *Resp {
	c.e.T.Helper()
	return c.Do("POST", path, body, "Idempotency-Key", key)
}

// Refresh memanggil /auth/refresh dengan header CSRF yang benar.
func (c *Client) Refresh() *Resp {
	c.e.T.Helper()
	return c.Do("POST", "/api/v1/auth/refresh", nil, "X-Requested-With", "tixgo", "Origin", "http://localhost:5174")
}

// Data membongkar {"data": ...} ke dst.
func (r *Resp) Data(dst any) *Resp {
	r.t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &env); err != nil {
		r.t.Fatalf("body bukan JSON envelope: %s", r.Body)
	}
	if dst != nil {
		if err := json.Unmarshal(env.Data, dst); err != nil {
			r.t.Fatalf("data tidak cocok: %v: %s", err, env.Data)
		}
	}
	return r
}

func (r *Resp) Map() map[string]any {
	r.t.Helper()
	var m map[string]any
	r.Data(&m)
	return m
}

// ErrCode mengembalikan error.code (kosong bila bukan error).
func (r *Resp) ErrCode() string {
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &env)
	return env.Error.Code
}

func (r *Resp) ErrDetails() map[string]any {
	var env struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &env)
	return env.Error.Details
}

// Expect memastikan status; gagal menampilkan body.
func (r *Resp) Expect(code int) *Resp {
	r.t.Helper()
	if r.Code != code {
		r.t.Fatalf("status = %d, mau %d; body: %s", r.Code, code, r.Body)
	}
	return r
}

func (r *Resp) ExpectErr(code int, errCode string) *Resp {
	r.t.Helper()
	if r.Code != code || r.ErrCode() != errCode {
		r.t.Fatalf("mau %d/%s, dapat %d/%s; body: %s", code, errCode, r.Code, r.ErrCode(), r.Body)
	}
	return r
}

// ---------- fixture pengguna ----------

var emailSeq int

func uniqEmail(prefix string) string {
	emailSeq++
	return fmt.Sprintf("%s%d@example.com", prefix, emailSeq)
}

// Register mendaftar lewat API dan mengembalikan client yang sudah login.
func (e *Env) Register(email, name string) *Client {
	e.T.Helper()
	c := e.Anon()
	r := c.POST("/api/v1/auth/register", map[string]any{"email": email, "password": testPassword, "name": name, "accept_terms": true}).Expect(201)
	var d struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	r.Data(&d)
	c.Token, c.Email, c.UserID = d.AccessToken, email, d.User.ID
	return c
}

var phoneSeq int

// Verified = pengguna yang sudah memverifikasi email & HP (jalan pintas SQL, bukan lewat OTP).
func (e *Env) Verified(name string) *Client {
	e.T.Helper()
	c := e.Register(uniqEmail("u"), name)
	phoneSeq++
	phone := fmt.Sprintf("+62812%08d", phoneSeq)
	if _, err := pool.Exec(e.ctx, "UPDATE users SET email_verified_at=now(), phone=$2, phone_verified_at=now() WHERE id=$1", c.UserID, phone); err != nil {
		e.T.Fatal(err)
	}
	return c
}

// UserWithRole membuat pengguna dengan peran tertentu, login lewat API. Peran admin/finance diberi MFA (login memakai TOTP).
func (e *Env) UserWithRole(role, name string) *Client {
	e.T.Helper()
	email := uniqEmail(role)
	c := e.Register(email, name)
	phoneSeq++
	if _, err := pool.Exec(e.ctx, "UPDATE users SET role=$2, email_verified_at=now(), phone=$3, phone_verified_at=now() WHERE id=$1", c.UserID, role, fmt.Sprintf("+62813%08d", phoneSeq)); err != nil {
		e.T.Fatal(err)
	}
	if service.MFARequiredRole(role) {
		secret := auth.NewTOTPSecret()
		enc, _ := e.Svc.Box.Encrypt([]byte(secret))
		if _, err := pool.Exec(e.ctx, "INSERT INTO mfa_totp (user_id, secret_enc, confirmed_at) VALUES ($1,$2,now())", c.UserID, enc); err != nil {
			e.T.Fatal(err)
		}
		_, _ = pool.Exec(e.ctx, "UPDATE users SET mfa_enabled=true WHERE id=$1", c.UserID)
		code, _ := auth.TOTPCode(secret, e.Clock.Now(), 30)
		r := e.Anon().POST("/api/v1/auth/login", map[string]any{"email": email, "password": testPassword, "mfa_code": code}).Expect(200)
		var d struct {
			AccessToken string `json:"access_token"`
		}
		r.Data(&d)
		c.Token = d.AccessToken
		return c
	}
	return e.Login(email)
}

// Login masuk ulang dengan sandi standar (mengambil klaim peran terbaru).
func (e *Env) Login(email string) *Client {
	e.T.Helper()
	c := e.Anon()
	r := c.POST("/api/v1/auth/login", map[string]any{"email": email, "password": testPassword}).Expect(200)
	var d struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	r.Data(&d)
	c.Token, c.Email, c.UserID = d.AccessToken, email, d.User.ID
	return c
}

// ---------- util ----------

func (e *Env) Q() *sqlcgen.Queries { return e.Svc.Q }

func (e *Env) Exec(sql string, args ...any) {
	e.T.Helper()
	if _, err := pool.Exec(e.ctx, sql, args...); err != nil {
		e.T.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *Env) QueryInt(sql string, args ...any) int64 {
	e.T.Helper()
	var n int64
	if err := pool.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.T.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func (e *Env) QueryStr(sql string, args ...any) string {
	e.T.Helper()
	var s string
	if err := pool.QueryRow(e.ctx, sql, args...).Scan(&s); err != nil {
		e.T.Fatalf("query %q: %v", sql, err)
	}
	return s
}

// TokenFromMail mengambil nilai query `token` dari tautan pada email terakhir ke alamat itu.
func (e *Env) TokenFromMail(to string) string {
	e.T.Helper()
	m, ok := e.Mail.LastMailTo(to)
	if !ok {
		e.T.Fatalf("tidak ada email ke %s", to)
	}
	i := strings.Index(m.Text, "token=")
	if i < 0 {
		e.T.Fatalf("tidak ada token pada email: %s", m.Text)
	}
	rest := m.Text[i+len("token="):]
	end := strings.IndexAny(rest, " \n\r\t")
	if end >= 0 {
		rest = rest[:end]
	}
	tok, _ := url.QueryUnescape(rest)
	return tok
}

func newID() string { return id.New() }

// jsonBody mengubah nilai menjadi map generik (mudah dipakai membangun body).
type M = map[string]any

// SeedLegal menerbitkan Syarat & Privasi (id) bagi tes yang membutuhkannya.
func (e *Env) SeedLegal() (termsID, privacyID string) {
	e.T.Helper()
	admin := e.UserWithRole("admin", "Admin Legal")
	for _, kind := range []string{"terms", "privacy"} {
		var d struct {
			ID string `json:"id"`
		}
		admin.POST("/api/v1/admin/legal", M{"kind": kind, "locale": "id", "title": kind + " v1", "body": "isi " + kind}).Expect(201).Data(&d)
		admin.POST("/api/v1/admin/legal/"+d.ID+"/publish", nil).Expect(200)
		if kind == "terms" {
			termsID = d.ID
		} else {
			privacyID = d.ID
		}
	}
	return
}
