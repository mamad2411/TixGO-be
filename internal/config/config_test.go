package config

import (
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":  "postgres://localhost:5432/db",
		"REDIS_ADDR":    "localhost:6379",
		"CORS_ORIGINS":  "http://localhost:5173, http://127.0.0.1:5173",
		"JWT_SECRET":    strings.Repeat("a", 32),
		"ENC_KEY":       strings.Repeat("b", 64),
		"S3_ENDPOINT":   "localhost:9000",
		"S3_BUCKET":     "tixgo",
		"S3_ACCESS_KEY": "k",
		"S3_SECRET_KEY": "s",
	}
}

func TestLoadValid(t *testing.T) {
	c, err := Load(envMap(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPAddr != ":8080" || len(c.CORSOrigins) != 2 {
		t.Fatalf("default/CSV salah: %+v", c)
	}
	if c.JWTAccessTTL.Minutes() != 15 {
		t.Fatalf("JWTAccessTTL = %v", c.JWTAccessTTL)
	}
}

func TestLoadFailsLoudlyWhenRequiredMissing(t *testing.T) {
	_, err := Load(envMap(map[string]string{}))
	if err == nil {
		t.Fatal("harus error saat env wajib hilang")
	}
	for _, k := range []string{"DATABASE_URL", "REDIS_ADDR", "JWT_SECRET", "ENC_KEY", "CORS_ORIGINS"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error tidak menyebut %s: %v", k, err)
		}
	}
}

func TestLoadRejectsWeakSecrets(t *testing.T) {
	e := validEnv()
	e["JWT_SECRET"] = "pendek"
	e["ENC_KEY"] = "bukan64"
	_, err := Load(envMap(e))
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") || !strings.Contains(err.Error(), "ENC_KEY") {
		t.Fatalf("harus menolak secret lemah: %v", err)
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	e := validEnv()
	e["JWT_ACCESS_TTL"] = "lima menit"
	if _, err := Load(envMap(e)); err == nil {
		t.Fatal("harus error untuk durasi tak valid")
	}
}

func TestProductionRequiresMetricsTokenAndRejectsDevOnlySwitches(t *testing.T) {
	base := map[string]string{"APP_ENV": "production", "DATABASE_URL": "x", "REDIS_ADDR": "x", "CORS_ORIGINS": "https://a.test", "JWT_SECRET": strings.Repeat("s", 40),
		"ENC_KEY": strings.Repeat("a", 64), "S3_ENDPOINT": "x", "S3_BUCKET": "x", "S3_ACCESS_KEY": "x", "S3_SECRET_KEY": "x", "MIDTRANS_SERVER_KEY": "k", "METRICS_TOKEN": "tok"}
	load := func(extra map[string]string) error {
		_, err := Load(func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		})
		return err
	}
	if err := load(nil); err != nil {
		t.Fatalf("konfigurasi production valid ditolak: %v", err)
	}
	for name, extra := range map[string]map[string]string{
		"tanpa METRICS_TOKEN": {"METRICS_TOKEN": ""},
		"gateway fake":        {"PAYMENT_GATEWAY": "fake"},
		"rate limit mati":     {"RATE_LIMIT_DISABLED": "true"},
	} {
		if err := load(extra); err == nil {
			t.Errorf("%s harus ditolak di production", name)
		}
	}
}
