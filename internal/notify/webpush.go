package notify

import (
	"context"
	"errors"
	"io"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// ErrPushGone = langganan sudah tidak valid (404/410) → hapus dari DB.
var ErrPushGone = errors.New("notify: langganan push tidak berlaku lagi")

// WebPush mengirim Web Push (VAPID). Tanpa kunci VAPID gunakan NoPush.
type WebPush struct{ Public, Private, Subject string }

func (w WebPush) Send(ctx context.Context, sub PushSub, payload []byte) error {
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}},
		&webpush.Options{Subscriber: w.Subject, VAPIDPublicKey: w.Public, VAPIDPrivateKey: w.Private, TTL: 3600, Urgency: webpush.UrgencyNormal})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrPushGone
	case resp.StatusCode >= 300:
		return errors.New("notify: push ditolak: " + resp.Status)
	}
	return nil
}
