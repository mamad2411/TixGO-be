package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/metrics"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// IntegrationService = API key organizer, webhook bertanda tangan, widget allowlist, piksel (docs/20 M38).
type IntegrationService struct {
	s      *Services
	client *http.Client
}

const jobWebhook = "webhook:deliver"

var (
	WebhookEvents = []string{"order.paid", "ticket.issued", "ticket.refunded"}
	APIScopes     = []string{"sales:read", "attendees:read"}
	// jeda sebelum percobaan ke-2..6 (retry berjenjang)
	webhookBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}
	domainRe       = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}$`)
	pixelRe        = regexp.MustCompile(`^[A-Za-z0-9_-]{4,40}$`)
)

func newIntegrationService(s *Services) *IntegrationService {
	i := &IntegrationService{s: s}
	i.client = &http.Client{
		Timeout: 8 * time.Second,
		// tolak pengalihan: mencegah SSRF lewat redirect ke host internal
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
				// Control dijalankan SETELAH DNS resolve → memeriksa IP sebenarnya (anti DNS-rebinding)
				Control: func(_, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					if ip := net.ParseIP(host); ip != nil && !s.Cfg.WebhookAllowPrivate && isInternalIP(ip) {
						return fmt.Errorf("tujuan webhook ke alamat internal diblokir")
					}
					return nil
				},
			}).DialContext,
			MaxIdleConns: 20, IdleConnTimeout: 30 * time.Second,
		},
	}
	s.Reg.Handle(jobWebhook, i.handleDeliver)
	return i
}

func isInternalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.Equal(net.ParseIP("169.254.169.254"))
}

func (i *IntegrationService) owner(sc OrgScope) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	return nil
}

// ---------- API key ----------

type APIKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Key        string     `json:"key,omitempty"` // hanya saat dibuat
}

func keyView(k sqlcgen.ApiKey) APIKeyView {
	return APIKeyView{ID: k.ID, Name: k.Name, Prefix: k.KeyPrefix, Scopes: k.Scopes, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt, CreatedAt: k.CreatedAt}
}

type APIKeyInput struct {
	Name   string   `json:"name" binding:"required,min=3,max=60"`
	Scopes []string `json:"scopes" binding:"required,min=1,dive,oneof=sales:read attendees:read"`
}

func (i *IntegrationService) CreateKey(ctx context.Context, p *auth.Principal, sc OrgScope, in APIKeyInput) (*APIKeyView, error) {
	if err := i.owner(sc); err != nil {
		return nil, err
	}
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	raw := "tgk_" + auth.RandomToken(32)
	var k sqlcgen.ApiKey
	err := i.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		k, err = q.InsertAPIKey(ctx, sqlcgen.InsertAPIKeyParams{ID: id.New(), OrganizerID: sc.OrganizerID, Name: in.Name, KeyHash: auth.HashToken(raw), KeyPrefix: raw[:10], Scopes: in.Scopes})
		if err != nil {
			return err
		}
		return i.s.Audit.Log(ctx, q, p, "apikey.create", "api_key", k.ID, nil, map[string]any{"scopes": in.Scopes})
	})
	if err != nil {
		return nil, err
	}
	v := keyView(k)
	v.Key = raw
	return &v, nil
}

func (i *IntegrationService) ListKeys(ctx context.Context, sc OrgScope) ([]APIKeyView, error) {
	rows, err := i.s.Q.ListAPIKeys(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	out := make([]APIKeyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, keyView(r))
	}
	return out, nil
}

func (i *IntegrationService) RevokeKey(ctx context.Context, p *auth.Principal, sc OrgScope, keyID string) error {
	if err := i.owner(sc); err != nil {
		return err
	}
	return i.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.RevokeAPIKey(ctx, sqlcgen.RevokeAPIKeyParams{ID: keyID, OrganizerID: sc.OrganizerID})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("API key")
		}
		return i.s.Audit.Log(ctx, q, p, "apikey.revoke", "api_key", keyID, nil, nil)
	})
}

// Rotate = cabut yang lama + buat baru dengan nama & scope sama.
func (i *IntegrationService) RotateKey(ctx context.Context, p *auth.Principal, sc OrgScope, keyID string) (*APIKeyView, error) {
	keys, err := i.s.Q.ListAPIKeys(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.ID == keyID && k.RevokedAt == nil {
			nv, err := i.CreateKey(ctx, p, sc, APIKeyInput{Name: k.Name, Scopes: k.Scopes})
			if err != nil {
				return nil, err
			}
			if err := i.RevokeKey(ctx, p, sc, keyID); err != nil {
				return nil, err
			}
			return nv, nil
		}
	}
	return nil, apperr.NotFound("API key")
}

// AuthenticateKey memverifikasi key mentah; mengembalikan organizer + scope. Rate limit per key.
type KeyIdentity struct {
	KeyID, OrganizerID string
	Scopes             map[string]bool
}

func (i *IntegrationService) AuthenticateKey(ctx context.Context, raw string) (*KeyIdentity, error) {
	if !strings.HasPrefix(raw, "tgk_") || len(raw) > 80 {
		return nil, apperr.Unauthorized("API key tidak valid")
	}
	k, err := i.s.Q.GetAPIKeyByHash(ctx, auth.HashToken(raw))
	if err != nil || k.OrganizerStatus != "active" {
		return nil, apperr.Unauthorized("API key tidak valid")
	}
	if ok, ra, err := i.s.Limiter.Allow(ctx, "apikey:"+k.ID, 120, time.Minute); err == nil && !ok {
		return nil, apperr.TooMany("batas permintaan API key tercapai").With("retry_after", int(ra.Seconds())+1)
	}
	_ = i.s.Q.TouchAPIKey(ctx, k.ID)
	sc := map[string]bool{}
	for _, s := range k.Scopes {
		sc[s] = true
	}
	return &KeyIdentity{KeyID: k.ID, OrganizerID: k.OrganizerID, Scopes: sc}, nil
}

type PartnerEvent struct {
	ID, Slug, Title, Status string
	StartsAt                time.Time
}

func (i *IntegrationService) PartnerEvents(ctx context.Context, ki *KeyIdentity) ([]map[string]any, error) {
	rows, err := i.s.Q.PartnerEvents(ctx, ki.OrganizerID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"id": r.ID, "slug": r.Slug, "title": r.Title, "status": r.Status, "starts_at": r.StartsAt})
	}
	return out, nil
}

func (i *IntegrationService) PartnerSales(ctx context.Context, ki *KeyIdentity, eventID string) (*SettlementReport, error) {
	return i.s.Finance.EventSettlement(ctx, OrgScope{OrganizerID: ki.OrganizerID, Role: "member"}, eventID)
}

func (i *IntegrationService) PartnerAttendees(ctx context.Context, ki *KeyIdentity, eventID string, f AttendeeQuery) ([]AttendeeView, int, error) {
	return i.s.Attendee.List(ctx, OrgScope{OrganizerID: ki.OrganizerID, Role: "member"}, eventID, f)
}

// ---------- webhook ----------

type WebhookView struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	Secret    string    `json:"secret,omitempty"` // hanya saat dibuat
}

type WebhookInput struct {
	URL    string   `json:"url" binding:"required,max=500"`
	Events []string `json:"events" binding:"required,min=1,dive,oneof=order.paid ticket.issued ticket.refunded"`
}

func (i *IntegrationService) validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return apperr.Validation("URL webhook tidak valid").With("field", "url")
	}
	if u.Scheme != "https" && (!i.s.Cfg.WebhookAllowPrivate || u.Scheme != "http") {
		return apperr.Validation("URL webhook harus https").With("field", "url")
	}
	h := u.Hostname()
	if !i.s.Cfg.WebhookAllowPrivate {
		if ip := net.ParseIP(h); ip != nil && isInternalIP(ip) {
			return apperr.Validation("URL webhook tidak boleh menuju alamat internal").With("field", "url")
		}
		if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
			return apperr.Validation("URL webhook tidak boleh menuju host internal").With("field", "url")
		}
	}
	return nil
}

func (i *IntegrationService) CreateWebhook(ctx context.Context, p *auth.Principal, sc OrgScope, in WebhookInput) (*WebhookView, error) {
	if err := i.owner(sc); err != nil {
		return nil, err
	}
	if err := i.validateURL(in.URL); err != nil {
		return nil, err
	}
	existing, _ := i.s.Q.ListWebhookEndpoints(ctx, sc.OrganizerID)
	if len(existing) >= 10 {
		return nil, apperr.Conflict(apperr.CodeLimitExceeded, "maksimal 10 endpoint webhook")
	}
	secret := "whsec_" + auth.RandomToken(24)
	enc, err := i.s.Box.Encrypt([]byte(secret))
	if err != nil {
		return nil, err
	}
	var w sqlcgen.WebhookEndpoint
	err = i.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		w, err = q.InsertWebhookEndpoint(ctx, sqlcgen.InsertWebhookEndpointParams{ID: id.New(), OrganizerID: sc.OrganizerID, Url: in.URL, SecretEnc: enc, Events: in.Events})
		if err != nil {
			return err
		}
		return i.s.Audit.Log(ctx, q, p, "webhook.create", "webhook", w.ID, nil, map[string]any{"url": in.URL, "events": in.Events})
	})
	if err != nil {
		return nil, err
	}
	return &WebhookView{ID: w.ID, URL: w.Url, Events: w.Events, Active: w.Active, CreatedAt: w.CreatedAt, Secret: secret}, nil
}

func (i *IntegrationService) ListWebhooks(ctx context.Context, sc OrgScope) ([]WebhookView, error) {
	rows, err := i.s.Q.ListWebhookEndpoints(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	out := make([]WebhookView, 0, len(rows))
	for _, w := range rows {
		out = append(out, WebhookView{ID: w.ID, URL: w.Url, Events: w.Events, Active: w.Active, CreatedAt: w.CreatedAt})
	}
	return out, nil
}

func (i *IntegrationService) DeleteWebhook(ctx context.Context, p *auth.Principal, sc OrgScope, whID string) error {
	if err := i.owner(sc); err != nil {
		return err
	}
	return i.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteWebhookEndpoint(ctx, sqlcgen.DeleteWebhookEndpointParams{ID: whID, OrganizerID: sc.OrganizerID})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("webhook")
		}
		return i.s.Audit.Log(ctx, q, p, "webhook.delete", "webhook", whID, nil, nil)
	})
}

func (i *IntegrationService) Deliveries(ctx context.Context, sc OrgScope, whID string) ([]sqlcgen.WebhookDelivery, error) {
	return i.s.Q.ListWebhookDeliveries(ctx, sqlcgen.ListWebhookDeliveriesParams{OrganizerID: sc.OrganizerID, ID: whID})
}

// Emit membuat pengiriman untuk semua endpoint organizer yang berlangganan. Aman dipanggil setelah commit; kegagalan hanya dicatat.
func (i *IntegrationService) Emit(ctx context.Context, organizerID, event string, data map[string]any) {
	eps, err := i.s.Q.ActiveEndpointsForEvent(ctx, sqlcgen.ActiveEndpointsForEventParams{Org: organizerID, Event: event})
	if err != nil {
		i.s.Log.Error("webhook: muat endpoint gagal", "error", err)
		return
	}
	for _, ep := range eps {
		i.enqueue(ctx, ep, event, data)
	}
}

func (i *IntegrationService) enqueue(ctx context.Context, ep sqlcgen.WebhookEndpoint, event string, data map[string]any) {
	body := mustJSON(map[string]any{"id": id.New(), "type": event, "created": i.s.now().Unix(), "data": data})
	d, err := i.s.Q.InsertWebhookDelivery(ctx, sqlcgen.InsertWebhookDeliveryParams{ID: id.New(), EndpointID: ep.ID, Event: event, Payload: body, NextAttemptAt: ptr(i.s.now())})
	if err != nil {
		i.s.Log.Error("webhook: simpan pengiriman gagal", "error", err)
		return
	}
	if err := i.s.Jobs.Enqueue(ctx, jobWebhook, map[string]string{"id": d.ID}, jobs.MaxRetry(0)); err != nil {
		i.s.Log.Error("webhook: enqueue gagal", "error", err)
	}
}

// Sign: header X-TixGo-Signature = t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>.
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	_, _ = m.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(m.Sum(nil)))
}

func (i *IntegrationService) handleDeliver(ctx context.Context, payload []byte) error {
	p, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil
	}
	d, err := i.s.Q.GetWebhookDelivery(ctx, p["id"])
	if err != nil || d.DeliveredAt != nil {
		return nil
	}
	ep, err := i.s.Q.GetWebhookEndpoint(ctx, d.EndpointID)
	if err != nil || !ep.Active {
		return nil
	}
	secret, err := i.s.Box.Decrypt(ep.SecretEnc)
	if err != nil {
		return nil
	}
	status, derr := i.post(ctx, ep.Url, string(secret), d.Event, d.ID, d.Payload)
	attempt := int(d.Attempts) + 1
	var st *int
	if status > 0 {
		st = &status
	}
	if derr == nil {
		metrics.Webhooks.WithLabelValues("organizer", "ok").Inc()
		return i.s.Q.RecordDeliveryAttempt(ctx, sqlcgen.RecordDeliveryAttemptParams{ID: d.ID, Status: st, DeliveredAt: ptr(i.s.now())})
	}
	metrics.Webhooks.WithLabelValues("organizer", "fail").Inc()
	if attempt > len(webhookBackoff) { // menyerah setelah 6 percobaan
		return i.s.Q.RecordDeliveryAttempt(ctx, sqlcgen.RecordDeliveryAttemptParams{ID: d.ID, Status: st})
	}
	delay := webhookBackoff[attempt-1]
	next := i.s.now().Add(delay)
	if err := i.s.Q.RecordDeliveryAttempt(ctx, sqlcgen.RecordDeliveryAttemptParams{ID: d.ID, Status: st, NextAttemptAt: &next}); err != nil {
		return err
	}
	return i.s.Jobs.Enqueue(ctx, jobWebhook, map[string]string{"id": d.ID}, jobs.In(delay), jobs.MaxRetry(0))
}

func (i *IntegrationService) post(ctx context.Context, rawURL, secret, event, deliveryID string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TixGo-Webhook/1")
	req.Header.Set("X-TixGo-Event", event)
	req.Header.Set("X-TixGo-Delivery", deliveryID)
	req.Header.Set("X-TixGo-Signature", Sign(secret, i.s.now().Unix(), body))
	resp, err := i.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("endpoint membalas %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Ping mengirim event uji sinkron ke endpoint dan melaporkan hasilnya.
func (i *IntegrationService) Ping(ctx context.Context, sc OrgScope, whID string) (map[string]any, error) {
	ep, err := i.s.Q.GetWebhookEndpoint(ctx, whID)
	if err != nil || ep.OrganizerID != sc.OrganizerID {
		return nil, apperr.NotFound("webhook")
	}
	secret, err := i.s.Box.Decrypt(ep.SecretEnc)
	if err != nil {
		return nil, err
	}
	body := mustJSON(map[string]any{"id": id.New(), "type": "ping", "created": i.s.now().Unix(), "data": map[string]any{}})
	status, derr := i.post(ctx, ep.Url, string(secret), "ping", "ping", body)
	res := map[string]any{"status": status, "ok": derr == nil}
	if derr != nil {
		res["error"] = derr.Error()
	}
	return res, nil
}

// ---------- widget & piksel ----------

func (i *IntegrationService) AddDomain(ctx context.Context, p *auth.Principal, sc OrgScope, domain string) error {
	if err := i.owner(sc); err != nil {
		return err
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !domainRe.MatchString(domain) {
		return apperr.Validation("domain tidak valid (contoh: tiket.contoh.com)").With("field", "domain")
	}
	return i.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.AddWidgetDomain(ctx, sqlcgen.AddWidgetDomainParams{OrganizerID: sc.OrganizerID, Domain: domain}); err != nil {
			return err
		}
		return i.s.Audit.Log(ctx, q, p, "widget.domain_add", "organizer", sc.OrganizerID, nil, map[string]any{"domain": domain})
	})
}

func (i *IntegrationService) Domains(ctx context.Context, sc OrgScope) ([]string, error) {
	d, err := i.s.Q.ListWidgetDomains(ctx, sc.OrganizerID)
	if d == nil {
		d = []string{}
	}
	return d, err
}

func (i *IntegrationService) RemoveDomain(ctx context.Context, p *auth.Principal, sc OrgScope, domain string) error {
	if err := i.owner(sc); err != nil {
		return err
	}
	n, err := i.s.Q.DeleteWidgetDomain(ctx, sqlcgen.DeleteWidgetDomainParams{OrganizerID: sc.OrganizerID, Domain: strings.ToLower(domain)})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("domain")
	}
	return nil
}

// WidgetOriginAllowed: origin (https://host) harus berada di allowlist organizer pemilik event.
func (i *IntegrationService) WidgetOriginAllowed(ctx context.Context, orgID, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	doms, err := i.s.Q.ListWidgetDomains(ctx, orgID)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range doms {
		if host == d {
			return true
		}
	}
	return false
}

// FrameAncestors = nilai CSP agar widget hanya bisa di-iframe dari domain allowlist.
func (i *IntegrationService) FrameAncestors(ctx context.Context, orgID string) string {
	doms, _ := i.s.Q.ListWidgetDomains(ctx, orgID)
	if len(doms) == 0 {
		return "frame-ancestors 'none'"
	}
	parts := []string{"frame-ancestors"}
	for _, d := range doms {
		parts = append(parts, "https://"+d)
	}
	return strings.Join(parts, " ")
}

type Tracking struct {
	GA     string `json:"ga_id,omitempty" binding:"omitempty"`
	Meta   string `json:"meta_pixel_id,omitempty"`
	TikTok string `json:"tiktok_pixel_id,omitempty"`
}

func (i *IntegrationService) SetTracking(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, t Tracking) (*Tracking, error) {
	for _, v := range []string{t.GA, t.Meta, t.TikTok} {
		if v != "" && !pixelRe.MatchString(v) {
			return nil, apperr.Validation("ID piksel hanya huruf/angka/_/- (4–40)").With("field", "tracking")
		}
	}
	err := i.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ev, err := i.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		if err := q.SetEventTracking(ctx, sqlcgen.SetEventTrackingParams{ID: ev.ID, Tracking: mustJSON(t)}); err != nil {
			return err
		}
		return i.s.Audit.Log(ctx, q, p, "event.tracking", "event", ev.ID, nil, t)
	})
	return &t, err
}

// PixelsFor: ID piksel hanya dikembalikan bila subjek memberi persetujuan cookie kategori pemasaran (M28 AC4).
func (i *IntegrationService) PixelsFor(ctx context.Context, slug, subject string) (*Tracking, error) {
	ev, err := i.s.Catalog.lookup(ctx, slug)
	if err != nil || (ev.Status != stPublished && ev.Status != stPaused) {
		return nil, apperr.NotFound("event")
	}
	ck, err := i.s.Legal.GetCookie(ctx, subject)
	if err != nil {
		return nil, err
	}
	if !ck.Marketing {
		return &Tracking{}, nil
	}
	var t Tracking
	_ = json.Unmarshal(ev.Tracking, &t)
	return &t, nil
}

var _ = platform.IsNoRows
