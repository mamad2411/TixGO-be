package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/repository/sqlcgen"
)

// AccountService = preferensi notifikasi, web push, riwayat notifikasi (docs/20 M42).
type AccountService struct{ s *Services }

func newAccountService(s *Services) *AccountService { return &AccountService{s: s} }

var prefCategories = []string{"marketing", "reminders", "follows", "broadcasts"}
var prefChannels = []string{"email", "push"}

type PrefsView map[string]map[string]bool // category → channel → enabled

// Prefs: default semua aktif untuk transaksional-terkait (reminders/follows/broadcasts); marketing default MATI (opt-in, UU PDP).
func (a *AccountService) Prefs(ctx context.Context, userID string) (PrefsView, error) {
	rows, err := a.s.Q.ListNotificationPrefs(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := PrefsView{}
	for _, c := range prefCategories {
		out[c] = map[string]bool{}
		for _, ch := range prefChannels {
			out[c][ch] = c != "marketing" && ch == "email"
		}
	}
	for _, r := range rows {
		if _, ok := out[r.Category]; ok {
			out[r.Category][r.Channel] = r.Enabled
		}
	}
	return out, nil
}

func (a *AccountService) SetPref(ctx context.Context, userID, category, channel string, enabled bool) error {
	okC, okH := false, false
	for _, c := range prefCategories {
		okC = okC || c == category
	}
	for _, c := range prefChannels {
		okH = okH || c == channel
	}
	if !okC || !okH {
		return apperr.Validation("kategori/kanal tidak dikenal")
	}
	return a.s.Q.UpsertNotificationPref(ctx, sqlcgen.UpsertNotificationPrefParams{UserID: userID, Category: category, Channel: channel, Enabled: enabled})
}

type NotificationView struct {
	ID        string    `json:"id"`
	Template  string    `json:"template"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// Title/Body dirender dari templat + payload (kosong bila templat tidak dikenal; klien jatuh ke label sendiri).
	Title string `json:"title"`
	Body  string `json:"body"`
	Read  bool   `json:"read"`
}

// Email berisi tautan rahasia tidak ditampilkan di kotak masuk.
var inboxHidden = map[string]bool{"verify_email": true, "reset_password": true, "email_change_confirm": true}

// notifBody mengambil paragraf isi pertama (setelah sapaan) sebagai ringkasan satu-dua kalimat.
func notifBody(text string) string {
	paras := strings.Split(strings.TrimSpace(text), "\n\n")
	if len(paras) > 1 {
		paras = paras[1:]
	}
	b := strings.TrimSpace(paras[0])
	if i := strings.Index(b, "\n"); i >= 0 {
		b = b[:i]
	}
	if r := []rune(b); len(r) > 180 {
		b = string(r[:177]) + "..."
	}
	return b
}

func (a *AccountService) Notifications(ctx context.Context, userID, locale string, limit, offset int) ([]NotificationView, error) {
	rows, err := a.s.Q.ListNotificationsByUser(ctx, sqlcgen.ListNotificationsByUserParams{UserID: &userID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]NotificationView, 0, len(rows))
	for _, r := range rows {
		if r.Template == "push" || r.Status == "suppressed" || inboxHidden[r.Template] {
			continue
		}
		v := NotificationView{ID: r.ID, Template: r.Template, Channel: r.Channel, Status: r.Status, CreatedAt: r.CreatedAt, Read: r.ReadAt != nil}
		var data map[string]any
		if json.Unmarshal(r.Payload, &data) == nil {
			if sub, text, _, err := notify.Render(r.Template, locale, data); err == nil {
				v.Title, v.Body = sub, notifBody(text)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *AccountService) UnreadNotifications(ctx context.Context, userID string) (int64, error) {
	return a.s.Q.CountUnreadNotifications(ctx, &userID)
}

// MarkNotificationsRead menandai ids (kosong = semua) sebagai sudah dibaca.
func (a *AccountService) MarkNotificationsRead(ctx context.Context, userID string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	_, err := a.s.Q.MarkNotificationsRead(ctx, sqlcgen.MarkNotificationsReadParams{Uid: &userID, Ids: ids})
	return err
}

type PushSubInput struct {
	Endpoint string `json:"endpoint" binding:"required,max=1000"`
	P256dh   string `json:"p256dh" binding:"required,max=200"`
	Auth     string `json:"auth" binding:"required,max=100"`
}

func (a *AccountService) PushSubscribe(ctx context.Context, userID string, in PushSubInput) error {
	u, err := url.Parse(in.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return apperr.Validation("endpoint push harus https").With("field", "endpoint")
	}
	keys, _ := json.Marshal(map[string]string{"P256dh": in.P256dh, "Auth": in.Auth})
	enc, err := a.s.Box.Encrypt(keys)
	if err != nil {
		return err
	}
	return a.s.Q.UpsertPushSub(ctx, sqlcgen.UpsertPushSubParams{ID: id.New(), UserID: userID, Endpoint: in.Endpoint, KeysEnc: enc})
}

func (a *AccountService) PushUnsubscribe(ctx context.Context, userID, endpoint string) error {
	_, err := a.s.Q.DeletePushSub(ctx, sqlcgen.DeletePushSubParams{UserID: userID, Endpoint: endpoint})
	return err
}

// VAPIDPublicKey untuk klien (null bila push tidak dikonfigurasi).
func (a *AccountService) VAPIDPublicKey() *string {
	if a.s.Cfg.VAPIDPublic == "" {
		return nil
	}
	k := a.s.Cfg.VAPIDPublic
	return &k
}
