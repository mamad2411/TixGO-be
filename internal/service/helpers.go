package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/reqmeta"
)

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("mustJSON: " + err.Error())
	}
	return b
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// emit menulis event ke outbox (dalam transaksi pemanggil) — dikirim ke WebSocket oleh relayer.
func emit(ctx context.Context, q *sqlcgen.Queries, channel, topic string, payload any) error {
	return q.InsertOutbox(ctx, sqlcgen.InsertOutboxParams{Channel: channel, Topic: topic, Payload: mustJSON(payload)})
}

// BusChannel = kanal Redis pub/sub tempat hub WebSocket mendengar.
const BusChannel = "tixgo:events"

type busMsg struct {
	Channel string          `json:"channel"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	TS      time.Time       `json:"ts"`
}

// publish mengirim event efemeral langsung ke Redis pub/sub (posisi antrean, dsb.; tanpa outbox).
func (s *Services) publish(ctx context.Context, channel, typ string, data any) {
	b, _ := json.Marshal(busMsg{Channel: channel, Type: typ, Data: mustJSON(data), TS: s.now()})
	if err := s.Redis.Publish(ctx, BusChannel, b).Err(); err != nil {
		s.Log.Error("publish bus gagal", "channel", channel, "error", err)
	}
}

func reqmetaFrom(ctx context.Context) reqmeta.Meta { return reqmeta.From(ctx) }

func pgxCollectEvents(rows pgx.Rows) ([]sqlcgen.Event, error) {
	return pgx.CollectRows(rows, pgx.RowToStructByName[sqlcgen.Event])
}

// normalizeBank merapikan input rekening (kode bank kapital, nomor tanpa spasi/strip, nama dipangkas).
func normalizeBank(bank, no, name string) (string, string, string) {
	return strings.ToUpper(strings.TrimSpace(bank)), strings.NewReplacer(" ", "", "-", "").Replace(no), strings.TrimSpace(name)
}
