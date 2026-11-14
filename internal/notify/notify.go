// Package notify = pengirim email/SMS/push di balik antarmuka + katalog templat (id/en).
package notify

import (
	"bytes"
	"context"
	"fmt"
	htmltpl "html/template"
	"log/slog"
	"strings"
	"sync"
	texttpl "text/template"
	"time"

	mail "github.com/wneessen/go-mail"
)

// Mail = satu email siap kirim.
type Mail struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

type Mailer interface {
	Send(ctx context.Context, m Mail) error
}

type SMSSender interface {
	Send(ctx context.Context, to, text string) error
}

// ---- SMTP ----

type SMTP struct {
	host, user, pass, from string
	port                   int
}

func NewSMTP(host string, port int, user, pass, from string) *SMTP {
	return &SMTP{host: host, port: port, user: user, pass: pass, from: from}
}

func (s *SMTP) Send(ctx context.Context, m Mail) error {
	opts := []mail.Option{mail.WithPort(s.port), mail.WithTLSPolicy(mail.TLSOpportunistic)}
	if s.user != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthPlain), mail.WithUsername(s.user), mail.WithPassword(s.pass))
	}
	c, err := mail.NewClient(s.host, opts...)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	msg := mail.NewMsg()
	if err := msg.From(s.from); err != nil {
		return err
	}
	if err := msg.To(m.To); err != nil {
		return err
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(mail.TypeTextPlain, m.Text)
	if m.HTML != "" {
		msg.AddAlternativeString(mail.TypeTextHTML, m.HTML)
	}
	return c.DialAndSendWithContext(ctx, msg)
}

// ---- Log (dev) ----

// LogMailer/LogSMS mencatat ke log (dev). Isi dicatat hanya di non-produksi oleh pemanggil.
type LogMailer struct{ Log *slog.Logger }

func (l LogMailer) Send(_ context.Context, m Mail) error {
	l.Log.Info("email (log)", "to", m.To, "subject", m.Subject, "text", m.Text)
	return nil
}

type LogSMS struct{ Log *slog.Logger }

func (l LogSMS) Send(_ context.Context, to, text string) error {
	l.Log.Info("sms (log)", "to", to, "text", text)
	return nil
}

// ---- Memory (tes) ----

type Memory struct {
	mu     sync.Mutex
	Mails  []Mail
	SMSs   []SMSMsg
	FailOn func(Mail) error
}

type SMSMsg struct{ To, Text string }

func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Send(_ context.Context, mm Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailOn != nil {
		if err := m.FailOn(mm); err != nil {
			return err
		}
	}
	m.Mails = append(m.Mails, mm)
	return nil
}

// SMS mengembalikan pengirim SMS yang menulis ke Memory yang sama.
func (m *Memory) SMS() SMSSender { return memSMS{m} }

type memSMS struct{ m *Memory }

func (s memSMS) Send(_ context.Context, to, text string) error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.m.SMSs = append(s.m.SMSs, SMSMsg{to, text})
	return nil
}

// LastMailTo mengembalikan email terakhir untuk alamat tertentu.
func (m *Memory) LastMailTo(to string) (Mail, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.Mails) - 1; i >= 0; i-- {
		if strings.EqualFold(m.Mails[i].To, to) {
			return m.Mails[i], true
		}
	}
	return Mail{}, false
}

func (m *Memory) MailsTo(to string) []Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Mail
	for _, x := range m.Mails {
		if strings.EqualFold(x.To, to) {
			out = append(out, x)
		}
	}
	return out
}

func (m *Memory) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Mails)
}

func (m *Memory) LastSMS(to string) (SMSMsg, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.SMSs) - 1; i >= 0; i-- {
		if m.SMSs[i].To == to {
			return m.SMSs[i], true
		}
	}
	return SMSMsg{}, false
}

// ---- Push ----

type PushSub struct {
	Endpoint string
	P256dh   string
	Auth     string
}

type PushSender interface {
	Send(ctx context.Context, sub PushSub, payload []byte) error
}

type NoPush struct{}

func (NoPush) Send(context.Context, PushSub, []byte) error { return nil }

// ---- Templat ----

type tpl struct{ Subject, Body string }

// Render menghasilkan subjek, teks, dan HTML untuk templat+locale. Data dipakai dengan sintaks {{.key}}.
func Render(name, locale string, data map[string]any) (subject, text, html string, err error) {
	byLocale, ok := catalog[name]
	if !ok {
		return "", "", "", fmt.Errorf("templat %q tidak ada", name)
	}
	t, ok := byLocale[locale]
	if !ok {
		t = byLocale["id"]
	}
	sub, err := execText(t.Subject, data)
	if err != nil {
		return "", "", "", err
	}
	body, err := execText(t.Body, data)
	if err != nil {
		return "", "", "", err
	}
	h, err := execHTML(t.Body, data)
	if err != nil {
		return "", "", "", err
	}
	wrapped := `<div style="font-family:Arial,sans-serif;max-width:560px;margin:auto;color:#121110">` +
		`<div style="background:#E32620;color:#fff;padding:12px 16px;font-weight:700;letter-spacing:.04em">TIXGO</div>` +
		`<div style="padding:16px;line-height:1.5">` + strings.ReplaceAll(h, "\n", "<br>") + `</div></div>`
	return sub, body, wrapped, nil
}

func execText(s string, data map[string]any) (string, error) {
	t, err := texttpl.New("t").Option("missingkey=zero").Parse(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

func execHTML(s string, data map[string]any) (string, error) {
	t, err := htmltpl.New("t").Option("missingkey=zero").Parse(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// HasTemplate memeriksa keberadaan templat.
func HasTemplate(name string) bool { _, ok := catalog[name]; return ok }

// Templates mengembalikan daftar nama templat (tes/admin).
func Templates() []string {
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	return out
}

// FormatIDR memformat sen rupiah → "Rp 1.500.000" (nilai di sistem = rupiah utuh).
func FormatIDR(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%d", v)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	res := "Rp " + string(out)
	if neg {
		res = "-" + res
	}
	return res
}

// FormatTime memformat waktu dalam zona tertentu (zona venue).
func FormatTime(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("02 Jan 2006 15:04 MST")
}
