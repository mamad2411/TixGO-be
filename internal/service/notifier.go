package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"tixgo/backend/internal/id"
	"tixgo/backend/internal/jobs"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/repository/sqlcgen"
)

const jobNotifySend = "notify:send"

// Notifier menyimpan notifikasi lalu mengantrekan pengirimannya (dikirim worker dengan retry).
type Notifier struct{ s *Services }

type NotifyReq struct {
	UserID   string // opsional; bila diisi dan To kosong → pakai email & locale user
	To       string
	Template string
	Locale   string
	Data     map[string]any
	// Category non-kosong = dapat dimatikan pengguna (marketing, reminders, follows, broadcasts).
	Category string
	Channel  string // default "email"
}

func newNotifier(s *Services) *Notifier {
	n := &Notifier{s: s}
	s.Reg.Handle(jobNotifySend, n.handleSend)
	return n
}

// Send aman dipanggil setelah commit; kegagalan enqueue dicatat tetapi tidak menggagalkan alur utama.
func (n *Notifier) Send(ctx context.Context, r NotifyReq) {
	if err := n.send(ctx, n.s.Q, r); err != nil {
		n.s.Log.Error("notifikasi gagal diantrekan", "template", r.Template, "error", err)
	}
}

func (n *Notifier) send(ctx context.Context, q *sqlcgen.Queries, r NotifyReq) error {
	if !notify.HasTemplate(r.Template) {
		return fmt.Errorf("templat %q tidak ada", r.Template)
	}
	channel := r.Channel
	if channel == "" {
		channel = "email"
	}
	to, locale := r.To, r.Locale
	if r.UserID != "" && (to == "" || locale == "") {
		u, err := q.GetUserByID(ctx, r.UserID)
		if err != nil {
			return fmt.Errorf("muat user: %w", err)
		}
		if to == "" {
			to = u.Email
		}
		if locale == "" {
			locale = u.Locale
		}
	}
	if locale == "" {
		locale = "id"
	}
	data := r.Data
	if data == nil {
		data = map[string]any{}
	}
	data["_locale"] = locale
	data["_category"] = r.Category
	payload, _ := json.Marshal(data)
	nid := id.New()
	if err := q.InsertNotification(ctx, sqlcgen.InsertNotificationParams{
		ID: nid, UserID: strPtr(r.UserID), Channel: channel, Template: r.Template, ToAddr: strPtr(to), Payload: payload,
	}); err != nil {
		return err
	}
	return n.s.Jobs.Enqueue(ctx, jobNotifySend, map[string]string{"id": nid}, jobs.MaxRetry(5))
}

func (n *Notifier) handleSend(ctx context.Context, payload []byte) error {
	p, err := jobs.Decode[map[string]string](payload)
	if err != nil {
		return nil // payload rusak: tidak ada gunanya retry
	}
	row, err := n.s.Q.GetNotification(ctx, p["id"])
	if err != nil {
		return err
	}
	if row.Status != "queued" {
		return nil // idempoten
	}
	var data map[string]any
	_ = json.Unmarshal(row.Payload, &data)
	locale, _ := data["_locale"].(string)
	category, _ := data["_category"].(string)
	if category != "" && row.UserID != nil {
		enabled, err := n.s.Q.GetNotificationPref(ctx, sqlcgen.GetNotificationPrefParams{UserID: *row.UserID, Category: category, Channel: row.Channel})
		if err == nil && !enabled {
			return n.s.Q.MarkNotificationSuppressed(ctx, row.ID)
		}
	}
	if row.Channel == "push" {
		return n.deliverPush(ctx, row, data)
	}
	subject, text, html, err := notify.Render(row.Template, locale, data)
	if err != nil {
		_ = n.s.Q.MarkNotificationFailed(ctx, sqlcgen.MarkNotificationFailedParams{ID: row.ID, Status: "failed", Error: strPtr(err.Error())})
		return nil
	}
	to := deref(row.ToAddr)
	switch row.Channel {
	case "email":
		err = n.s.Mailer.Send(ctx, notify.Mail{To: to, Subject: subject, Text: text, HTML: html})
	case "sms":
		err = n.s.SMS.Send(ctx, to, text)
	default:
		return nil
	}
	if err != nil {
		// attempts naik; status tetap queued agar retry berikutnya bisa mengirim ulang
		_ = n.s.Q.MarkNotificationFailed(ctx, sqlcgen.MarkNotificationFailedParams{ID: row.ID, Status: "queued", Error: strPtr(err.Error())})
		return err
	}
	return n.s.Q.MarkNotificationSent(ctx, row.ID)
}

// SendPush mengantrekan web push ke semua perangkat pengguna (opt-in: tanpa langganan = tidak ada yang dikirim).
func (n *Notifier) SendPush(ctx context.Context, userID, category, title, body, url string) {
	n.Send(ctx, NotifyReq{UserID: userID, Channel: "push", Template: "push", Category: category,
		Data: map[string]any{"title": title, "body": body, "url": url}})
}

func (n *Notifier) deliverPush(ctx context.Context, row sqlcgen.Notification, data map[string]any) error {
	if row.UserID == nil {
		return n.s.Q.MarkNotificationSuppressed(ctx, row.ID)
	}
	subs, err := n.s.Q.ListPushSubs(ctx, *row.UserID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return n.s.Q.MarkNotificationSuppressed(ctx, row.ID)
	}
	payload, _ := json.Marshal(map[string]any{"title": data["title"], "body": data["body"], "url": data["url"]})
	sent := 0
	for _, s := range subs {
		raw, err := n.s.Box.Decrypt(s.KeysEnc)
		if err != nil {
			continue
		}
		var k struct{ P256dh, Auth string }
		if json.Unmarshal(raw, &k) != nil {
			continue
		}
		err = n.s.Push.Send(ctx, notify.PushSub{Endpoint: s.Endpoint, P256dh: k.P256dh, Auth: k.Auth}, payload)
		switch {
		case err == nil:
			sent++
		case errors.Is(err, notify.ErrPushGone):
			_, _ = n.s.Q.DeletePushSub(ctx, sqlcgen.DeletePushSubParams{UserID: *row.UserID, Endpoint: s.Endpoint})
		default:
			n.s.Log.Warn("push gagal", "error", err)
		}
	}
	if sent == 0 {
		return n.s.Q.MarkNotificationSuppressed(ctx, row.ID)
	}
	return n.s.Q.MarkNotificationSent(ctx, row.ID)
}
