package service

import (
	"encoding/json"

	"context"
	"time"
	"tixgo/backend/internal/repository/sqlcgen"
)

// RelayOutbox memindahkan baris outbox ke bus Redis (at-least-once; konsumen harus idempoten). Mengembalikan jumlah terkirim.
func (s *Services) RelayOutbox(ctx context.Context) (int, error) {
	n := 0
	err := s.InTx(ctx, func(q *sqlcgen.Queries) error {
		rows, err := q.ClaimOutbox(ctx, 200)
		if err != nil || len(rows) == 0 {
			return err
		}
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			s.publish(ctx, r.Channel, r.Topic, json.RawMessage(r.Payload))
			ids = append(ids, r.ID)
		}
		n = len(ids)
		return q.MarkOutboxPublished(ctx, ids)
	})
	return n, err
}

// RunSchedulers menjalankan semua loop periodik sampai ctx selesai. Aman dijalankan di beberapa worker (kunci Redis per loop).
func (s *Services) RunSchedulers(ctx context.Context) {
	type loop struct {
		name  string
		every time.Duration
		fn    func(context.Context) (int, error)
	}
	loops := []loop{
		{"queue_tick", 2 * time.Second, s.Queue.TickAll},
		{"outbox", 500 * time.Millisecond, s.RelayOutbox},
		{"holds_expire", 5 * time.Second, s.Hold.ExpireDue},
		{"orders_expire", 10 * time.Second, s.Order.ExpireDue},
		{"funnel_flush", 10 * time.Second, s.Analytics.Flush},
		{"payment_reconcile", time.Minute, s.Payment.Reconcile},
		{"refund_process", time.Minute, s.Refund.ProcessOpen},
		{"privacy_purge", time.Hour, s.Privacy.Purge},
		{"support_sweep", 10 * time.Minute, s.Support.Sweep},
		{"cms_scheduled", time.Minute, s.CMS.RunScheduled},
		{"reminders", time.Minute, s.Follow.RunReminders},
		{"resale_release", time.Minute, s.Resale.ReleaseStale},
		{"squad_sweep", time.Minute, s.Squad.Sweep},
		{"refund_overdue", time.Hour, s.Refund.EscalateOverdue},
		{"event_reminders", time.Hour, s.Follow.RunEventReminders},
		{"cancel_sweep", time.Minute, s.Lifecycle.SweepCancelled},
		{"settlement_release", 5 * time.Minute, s.Finance.ReleaseSettlements},
		{"events_finish", time.Minute, s.Event.RunFinish},
		{"events_publish", 30 * time.Second, s.Event.RunScheduledPublish},
	}
	for _, l := range loops {
		go func(l loop) {
			t := time.NewTicker(l.every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				ttl := l.every
				if ttl < time.Second {
					ttl = time.Second
				}
				ok, err := s.Redis.SetNX(ctx, "lock:sched:"+l.name, "1", ttl).Result()
				if err != nil || !ok {
					continue
				}
				if _, err := l.fn(ctx); err != nil && ctx.Err() == nil {
					s.Log.Error("scheduler gagal", "loop", l.name, "error", err)
				}
			}
		}(l)
	}
}
