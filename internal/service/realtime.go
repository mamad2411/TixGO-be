package service

import (
	"context"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
)

const wsTicketTTL = 60 * time.Second

// WSTicket menerbitkan tiket sekali-pakai 60 dtk untuk membuka WebSocket (browser tak bisa mengirim header Authorization).
func (s *Services) WSTicket(ctx context.Context, userID string) (string, error) {
	t := auth.RandomToken(24)
	if err := s.Redis.Set(ctx, "ws:ticket:"+auth.HashToken(t), userID, wsTicketTTL).Err(); err != nil {
		return "", apperr.Internal(err)
	}
	return t, nil
}

// ConsumeWSTicket menukar tiket menjadi userID ("" bila tak valid). Sekali pakai.
func (s *Services) ConsumeWSTicket(ctx context.Context, ticket string) (string, bool) {
	if ticket == "" || len(ticket) > 128 {
		return "", false
	}
	v, err := s.Redis.GetDel(ctx, "ws:ticket:"+auth.HashToken(ticket)).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

// AuthorizeChannel menentukan siapa boleh berlangganan kanal bus:
//
//	event.<id>        publik untuk event terbit (ketersediaan kursi, jeda penjualan)
//	queue.<id>        publik (tick/opened); queue.<id>.<userID> hanya pemilik
//	user.<id>         hanya pemilik
//	event.<id>.gate   staf gate/organizer event tsb
//	org.<eventID>     anggota organizer pemilik event (notifikasi penjualan)
func (s *Services) AuthorizeChannel(ctx context.Context, userID, channel string) bool {
	parts := strings.Split(channel, ".")
	if len(parts) < 2 || !id.Valid(parts[1]) && parts[0] != "user" {
		return false
	}
	switch {
	case parts[0] == "user" && len(parts) == 2:
		return userID != "" && parts[1] == userID
	case parts[0] == "queue" && len(parts) == 2:
		return s.publicEvent(ctx, parts[1])
	case parts[0] == "queue" && len(parts) == 3:
		return userID != "" && parts[2] == userID && s.publicEvent(ctx, parts[1])
	case parts[0] == "event" && len(parts) == 2:
		return s.publicEvent(ctx, parts[1])
	case parts[0] == "event" && len(parts) == 3 && parts[2] == "gate":
		if userID == "" {
			return false
		}
		_, err := s.Gate.authorize(ctx, userID, parts[1])
		return err == nil
	case parts[0] == "org" && len(parts) == 2:
		if userID == "" {
			return false
		}
		ev, err := s.Q.GetEvent(ctx, parts[1])
		if err != nil {
			return false
		}
		sc, _, err := s.Organizer.Membership(ctx, userID)
		return err == nil && sc != nil && sc.OrganizerID == ev.OrganizerID
	}
	return false
}

func (s *Services) publicEvent(ctx context.Context, eventID string) bool {
	if !id.Valid(eventID) {
		return false
	}
	var ev sqlcgen.Event
	ev, err := s.Q.GetEvent(ctx, eventID)
	return err == nil && ev.Visibility == "public" && (ev.Status == stPublished || ev.Status == stPaused)
}
