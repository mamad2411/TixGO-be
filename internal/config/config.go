package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config memuat seluruh env backend (docs/13-CONFIG-DEPLOY-TESTING.md §2, backend/docs/03-ENV.md).
// Nilai wajib yang hilang membuat startup GAGAL KERAS — tanpa default diam-diam.
type Config struct {
	AppEnv         string
	HTTPAddr       string
	DatabaseURL    string
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	CORSOrigins    []string
	TrustedProxies []string
	LogLevel       string

	PublicWebURL string // dipakai pada tautan email (verifikasi, reset, klaim)
	PublicAPIURL string

	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	EncKey        string

	ArgonMemoryKB     uint32
	ArgonTime         uint32
	WorkerConcurrency int
	ArgonThreads      uint8

	EmailTokenTTL time.Duration
	ResetTokenTTL time.Duration
	OTPTTL        time.Duration
	OTPMaxTries   int

	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool

	MidtransServerKey   string
	MidtransClientKey   string
	MidtransEnv         string // sandbox|production
	PaymentGateway      string // midtrans | xendit | fake (fake hanya non-production)
	XenditSecretKey     string
	XenditCallbackToken string
	XenditBaseURL       string
	RateLimitDisabled   bool   // hanya uji beban non-production
	MidtransSnapURL     string // override (tes)
	MidtransCoreURL     string // override (tes)

	QueueAdmitRate      int
	QueueAdmitWindowSec int
	QueuePreOpenMin     int
	HoldTTLSec          int
	HoldExtendSec       int
	TicketQRStepSec     int
	TicketMaxTransfers  int

	PayoutMode                 string // manual (auto belum diimplementasi)
	PayoutHoldDays             int
	PayoutMinAmount            int64
	ResaleFeeBP                int
	RefundDefaultPolicy        string
	RefundRescheduleWindowDays int
	RefundSLADays              int
	RefundAccountDeadlineDays  int

	PrivacyExportTTL time.Duration
	DSRSLADays       int

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string

	InvoiceSellerName    string
	InvoiceSellerAddress string

	SEOSiteURL   string
	SEOIndexHTML string // berkas index.html hasil build FE (shell untuk meta/JSON-LD)

	VAPIDPublic                string
	WebhookAllowPrivate        bool
	PrivacyContactEmail        string
	RetentionNotificationsDays int
	VAPIDPrivate               string
	VAPIDSubject               string

	MetricsToken string // bila diisi, /metrics butuh Bearer token
}

// Load membaca env lewat getenv (bisa diganti di test).
func Load(getenv func(string) string) (*Config, error) {
	var errs []error
	req := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			errs = append(errs, fmt.Errorf("env %s wajib diisi", key))
		}
		return v
	}
	opt := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	dur := func(key, def string) time.Duration {
		raw := opt(key, def)
		d, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("env %s bukan durasi valid: %q", key, raw))
		}
		return d
	}
	num := func(key string, def int) int {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("env %s bukan angka: %q", key, raw))
			return def
		}
		return n
	}
	oneOf := func(key, def string, allowed ...string) string {
		v := opt(key, def)
		for _, a := range allowed {
			if v == a {
				return v
			}
		}
		errs = append(errs, fmt.Errorf("env %s harus salah satu dari %v, dapat %q", key, allowed, v))
		return def
	}

	c := &Config{
		AppEnv:         opt("APP_ENV", "dev"),
		HTTPAddr:       opt("HTTP_ADDR", ":8080"),
		DatabaseURL:    req("DATABASE_URL"),
		RedisAddr:      req("REDIS_ADDR"),
		RedisPassword:  getenv("REDIS_PASSWORD"),
		RedisDB:        num("REDIS_DB", 0),
		CORSOrigins:    splitCSV(req("CORS_ORIGINS")),
		TrustedProxies: splitCSV(getenv("TRUSTED_PROXIES")),
		LogLevel:       opt("LOG_LEVEL", "info"),

		PublicWebURL: strings.TrimRight(opt("PUBLIC_WEB_URL", "http://localhost:5180"), "/"),
		PublicAPIURL: strings.TrimRight(opt("PUBLIC_API_URL", "http://localhost:8081"), "/"),

		JWTSecret:     req("JWT_SECRET"),
		JWTAccessTTL:  dur("JWT_ACCESS_TTL", "15m"),
		JWTRefreshTTL: dur("JWT_REFRESH_TTL", "720h"),
		EncKey:        req("ENC_KEY"),

		ArgonMemoryKB:     uint32(num("ARGON_MEMORY_KB", 64*1024)),
		ArgonTime:         uint32(num("ARGON_TIME", 2)),
		WorkerConcurrency: int(num("WORKER_CONCURRENCY", 20)),
		ArgonThreads:      uint8(num("ARGON_THREADS", 2)),

		EmailTokenTTL: dur("EMAIL_TOKEN_TTL", "24h"),
		ResetTokenTTL: dur("RESET_TOKEN_TTL", "30m"),
		OTPTTL:        dur("OTP_TTL", "5m"),
		OTPMaxTries:   num("OTP_MAX_TRIES", 5),

		S3Endpoint:  req("S3_ENDPOINT"),
		S3Bucket:    req("S3_BUCKET"),
		S3AccessKey: req("S3_ACCESS_KEY"),
		S3SecretKey: req("S3_SECRET_KEY"),

		MidtransServerKey:   getenv("MIDTRANS_SERVER_KEY"),
		MidtransClientKey:   getenv("MIDTRANS_CLIENT_KEY"),
		MidtransEnv:         oneOf("MIDTRANS_ENV", "sandbox", "sandbox", "production"),
		PaymentGateway:      oneOf("PAYMENT_GATEWAY", "midtrans", "midtrans", "xendit", "fake"),
		XenditSecretKey:     getenv("XENDIT_SECRET_KEY"),
		XenditCallbackToken: getenv("XENDIT_CALLBACK_TOKEN"),
		XenditBaseURL:       getenv("XENDIT_BASE_URL"),
		RateLimitDisabled:   getenv("RATE_LIMIT_DISABLED") == "true",
		MidtransSnapURL:     getenv("MIDTRANS_SNAP_URL"),
		MidtransCoreURL:     getenv("MIDTRANS_CORE_URL"),

		QueueAdmitRate:      num("QUEUE_ADMIT_RATE", 50),
		QueueAdmitWindowSec: num("QUEUE_ADMIT_WINDOW_SEC", 600),
		QueuePreOpenMin:     num("QUEUE_PRE_OPEN_MIN", 30),
		HoldTTLSec:          num("HOLD_TTL_SEC", 600),
		HoldExtendSec:       num("HOLD_EXTEND_SEC", 120),
		TicketQRStepSec:     num("TICKET_QR_STEP_SEC", 30),
		TicketMaxTransfers:  int(num("TICKET_MAX_TRANSFERS", 3)),

		PayoutMode:                 oneOf("PAYOUT_MODE", "manual", "manual"),
		PayoutHoldDays:             num("PAYOUT_HOLD_DAYS", 3),
		PayoutMinAmount:            int64(num("PAYOUT_MIN_AMOUNT", 50000)),
		ResaleFeeBP:                int(num("RESALE_FEE_BP", 500)),
		RefundDefaultPolicy:        oneOf("REFUND_DEFAULT_POLICY", "non_refundable", "non_refundable", "refundable"),
		RefundRescheduleWindowDays: num("REFUND_RESCHEDULE_WINDOW_DAYS", 14),
		RefundSLADays:              num("REFUND_SLA_DAYS", 14),
		RefundAccountDeadlineDays:  num("REFUND_ACCOUNT_DEADLINE_DAYS", 30),

		PrivacyExportTTL: dur("PRIVACY_EXPORT_TTL", "168h"),
		DSRSLADays:       num("PRIVACY_DSR_SLA_DAYS", 14),

		SMTPHost: getenv("SMTP_HOST"),
		SMTPPort: num("SMTP_PORT", 587),
		SMTPUser: getenv("SMTP_USER"),
		SMTPPass: getenv("SMTP_PASS"),
		SMTPFrom: opt("SMTP_FROM", "TixGo <no-reply@tixgo.local>"),

		InvoiceSellerName:    opt("INVOICE_SELLER_NAME", "TixGo"),
		InvoiceSellerAddress: opt("INVOICE_SELLER_ADDRESS", ""),

		SEOSiteURL:   strings.TrimRight(opt("SEO_SITE_URL", "http://localhost:5174"), "/"),
		SEOIndexHTML: getenv("SEO_INDEX_HTML"),

		VAPIDPublic:                getenv("VAPID_PUBLIC_KEY"),
		PrivacyContactEmail:        getenv("PRIVACY_CONTACT_EMAIL"),
		RetentionNotificationsDays: int(num("RETENTION_NOTIFICATIONS_DAYS", 180)),
		WebhookAllowPrivate:        getenv("WEBHOOK_ALLOW_PRIVATE") == "true" && opt("APP_ENV", "dev") != "production",
		VAPIDPrivate:               getenv("VAPID_PRIVATE_KEY"),
		VAPIDSubject:               opt("VAPID_SUBJECT", "mailto:admin@tixgo.local"),

		MetricsToken: getenv("METRICS_TOKEN"),
	}
	if v := getenv("S3_USE_SSL"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("env S3_USE_SSL bukan boolean: %q", v))
		}
		c.S3UseSSL = b
	}
	if len(c.JWTSecret) > 0 && len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("env JWT_SECRET minimal 32 karakter"))
	}
	if len(c.EncKey) > 0 && len(c.EncKey) != 64 {
		errs = append(errs, errors.New("env ENC_KEY harus 64 karakter hex (32 byte)"))
	}
	if c.AppEnv == "production" && c.PaymentGateway == "midtrans" && c.MidtransServerKey == "" {
		errs = append(errs, errors.New("env MIDTRANS_SERVER_KEY wajib di production"))
	}
	if c.PaymentGateway == "xendit" && (c.XenditSecretKey == "" || c.XenditCallbackToken == "") {
		errs = append(errs, errors.New("PAYMENT_GATEWAY=xendit butuh XENDIT_SECRET_KEY dan XENDIT_CALLBACK_TOKEN"))
	}
	if c.AppEnv == "production" && c.MetricsToken == "" {
		errs = append(errs, errors.New("env METRICS_TOKEN wajib di production (/metrics tidak boleh terbuka)"))
	}
	if c.RateLimitDisabled && c.AppEnv == "production" {
		errs = append(errs, errors.New("RATE_LIMIT_DISABLED dilarang di production"))
	}
	if c.PaymentGateway == "fake" && c.AppEnv == "production" {
		errs = append(errs, errors.New("PAYMENT_GATEWAY=fake dilarang di production"))
	}
	if c.QueueAdmitRate < 1 || c.HoldTTLSec < 60 || c.TicketQRStepSec < 10 {
		errs = append(errs, errors.New("QUEUE_ADMIT_RATE>=1, HOLD_TTL_SEC>=60, TICKET_QR_STEP_SEC>=10"))
	}
	if c.RetentionNotificationsDays < 30 || c.PayoutMinAmount < 1 || c.TicketMaxTransfers < 0 || c.WorkerConcurrency < 1 {
		errs = append(errs, errors.New("RETENTION_NOTIFICATIONS_DAYS>=30, PAYOUT_MIN_AMOUNT>=1, TICKET_MAX_TRANSFERS>=0, WORKER_CONCURRENCY>=1"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// FromOS memuat dari environment proses.
func FromOS() (*Config, error) { return Load(os.Getenv) }

// IsProd = lingkungan produksi (pengaturan yang lebih ketat).
func (c *Config) IsProd() bool { return c.AppEnv == "production" }

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
