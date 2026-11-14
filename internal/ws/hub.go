// Package ws: hub WebSocket (melody) yang meneruskan pesan bus Redis "tixgo:events" ke sesi yang berlangganan kanal tertentu.
// Pesan klien: {"op":"sub"|"unsub","channel":"event.<id>"}. Otorisasi per kanal dilakukan Authorizer (docs/04 §7).
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/olahol/melody"
	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/metrics"
)

// Authorizer memutuskan apakah userID ("" = anonim) boleh berlangganan kanal.
type Authorizer func(ctx context.Context, userID, channel string) bool

const (
	maxSubs     = 20
	maxMsgBytes = 2048
)

type Hub struct {
	m    *melody.Melody
	rdb  *redis.Client
	auth Authorizer
	log  *slog.Logger
	bus  string

	mu   sync.RWMutex
	subs map[*melody.Session]map[string]struct{}
}

// New membuat hub. allowedOrigins kosong = hanya same-origin; "*" tidak didukung (sengaja).
func New(rdb *redis.Client, bus string, auth Authorizer, allowedOrigins []string, log *slog.Logger) *Hub {
	h := &Hub{m: melody.New(), rdb: rdb, auth: auth, log: log, bus: bus, subs: map[*melody.Session]map[string]struct{}{}}
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	h.m.Upgrader = &websocket.Upgrader{
		ReadBufferSize: 1024, WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			if o == "" {
				return true // klien non-browser (tes, alat)
			}
			return allowed[o] || o == "http://"+r.Host || o == "https://"+r.Host
		},
	}
	h.m.Config.MaxMessageSize = maxMsgBytes
	h.m.HandleConnect(func(s *melody.Session) {
		h.mu.Lock()
		h.subs[s] = map[string]struct{}{}
		h.mu.Unlock()
		metrics.WSConns.Inc()
	})
	h.m.HandleDisconnect(func(s *melody.Session) {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
		metrics.WSConns.Dec()
	})
	h.m.HandleMessage(h.onMessage)
	return h
}

// Serve meng-upgrade koneksi. userID kosong = anonim (hanya kanal publik).
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID string) error {
	return h.m.HandleRequestWithKeys(w, r, map[string]any{"uid": userID})
}

func (h *Hub) reply(s *melody.Session, typ, channel, errMsg string) {
	b, _ := json.Marshal(map[string]string{"type": typ, "channel": channel, "error": errMsg})
	_ = s.Write(b)
}

func (h *Hub) onMessage(s *melody.Session, raw []byte) {
	var in struct{ Op, Channel string }
	if json.Unmarshal(raw, &in) != nil || in.Channel == "" || len(in.Channel) > 100 {
		h.reply(s, "error", in.Channel, "pesan tidak valid")
		return
	}
	uid, _ := s.Get("uid")
	u, _ := uid.(string)
	switch in.Op {
	case "sub":
		h.mu.RLock()
		n := len(h.subs[s])
		h.mu.RUnlock()
		if n >= maxSubs {
			h.reply(s, "error", in.Channel, "terlalu banyak langganan")
			return
		}
		if !h.auth(context.Background(), u, in.Channel) {
			h.reply(s, "error", in.Channel, "tidak diizinkan")
			return
		}
		h.mu.Lock()
		if set, ok := h.subs[s]; ok {
			set[in.Channel] = struct{}{}
		}
		h.mu.Unlock()
		h.reply(s, "subscribed", in.Channel, "")
	case "unsub":
		h.mu.Lock()
		delete(h.subs[s], in.Channel)
		h.mu.Unlock()
		h.reply(s, "unsubscribed", in.Channel, "")
	case "ping":
		h.reply(s, "pong", "", "")
	default:
		h.reply(s, "error", in.Channel, "op tidak dikenal")
	}
}

// Run mendengarkan bus Redis sampai ctx selesai; menyambung ulang otomatis bila putus.
func (h *Hub) Run(ctx context.Context) {
	for ctx.Err() == nil {
		sub := h.rdb.Subscribe(ctx, h.bus)
		ch := sub.Channel()
	loop:
		for {
			select {
			case <-ctx.Done():
				_ = sub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					break loop
				}
				h.dispatch([]byte(msg.Payload))
			}
		}
		_ = sub.Close()
		h.log.Warn("bus WS terputus; menyambung ulang")
		select {
		case <-ctx.Done():
			return
		case <-timeAfter():
		}
	}
}

func (h *Hub) dispatch(payload []byte) {
	var env struct {
		Channel string `json:"channel"`
	}
	if json.Unmarshal(payload, &env) != nil || env.Channel == "" {
		return
	}
	_ = h.m.BroadcastFilter(payload, func(s *melody.Session) bool {
		h.mu.RLock()
		_, ok := h.subs[s][env.Channel]
		h.mu.RUnlock()
		return ok
	})
}

// Close menutup semua koneksi.
func (h *Hub) Close() { _ = h.m.Close() }

// Connections = jumlah sesi aktif.
func (h *Hub) Connections() int { return h.m.Len() }

func timeAfter() <-chan time.Time { return time.After(2 * time.Second) }
