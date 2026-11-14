package apitest

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialWS(t *testing.T, srv *httptest.Server, ticket string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	if ticket != "" {
		u += "?ticket=" + ticket
	}
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readWS(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

func TestWebSocketSubscriptionsAuthorizationAndDelivery(t *testing.T) {
	e := NewEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Svc.WS.Run(ctx)
	srv := httptest.NewServer(e.Engine)
	defer srv.Close()

	_, evID, _, _ := e.publishedGA(100000, 10)
	u := e.Verified("Realtime")
	other := e.Verified("Orang Lain")

	var tk struct{ Ticket string }
	u.POST(api+"/ws/ticket", nil).Expect(200).Data(&tk)
	c := dialWS(t, srv, tk.Ticket)
	// tiket sekali pakai
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws?ticket="+tk.Ticket, nil); err == nil {
		t.Fatal("tiket yang sama tidak boleh dipakai dua kali")
	}
	send := func(c *websocket.Conn, op, ch string) { _ = c.WriteJSON(map[string]string{"op": op, "channel": ch}) }

	send(c, "sub", "user."+u.UserID)
	if m := readWS(t, c); m["type"] != "subscribed" {
		t.Fatalf("kanal sendiri: %v", m)
	}
	send(c, "sub", "user."+other.UserID)
	if m := readWS(t, c); m["type"] != "error" {
		t.Fatalf("kanal orang lain harus ditolak: %v", m)
	}
	send(c, "sub", "event."+evID+".gate")
	if m := readWS(t, c); m["type"] != "error" {
		t.Fatalf("gate hanya untuk staf: %v", m)
	}
	send(c, "sub", "event."+evID)
	if m := readWS(t, c); m["type"] != "subscribed" {
		t.Fatalf("event publik: %v", m)
	}
	send(c, "sub", "bukan-kanal")
	if m := readWS(t, c); m["type"] != "error" {
		t.Fatalf("kanal tak valid: %v", m)
	}

	pub := func(ch, typ string) {
		b, _ := json.Marshal(map[string]any{"channel": ch, "type": typ, "data": map[string]any{"x": 1}, "ts": time.Now()})
		e.Redis.Publish(ctx, "tixgo:events", b)
	}
	pub("user."+other.UserID, "secret") // tidak boleh sampai
	pub("event."+evID, "seat.updated")
	m := readWS(t, c)
	if m["type"] != "seat.updated" {
		t.Fatalf("hanya pesan kanal yang dilanggan yang sampai: %v", m)
	}
	pub("user."+u.UserID, "refund.done")
	if m := readWS(t, c); m["type"] != "refund.done" {
		t.Fatalf("pesan pribadi: %v", m)
	}
	// anonim: kanal publik ya, pribadi tidak
	a := dialWS(t, srv, "")
	send(a, "sub", "user."+u.UserID)
	if m := readWS(t, a); m["type"] != "error" {
		t.Fatalf("anonim tak boleh kanal pribadi: %v", m)
	}
	send(a, "sub", "event."+evID)
	if m := readWS(t, a); m["type"] != "subscribed" {
		t.Fatalf("anonim boleh kanal publik: %v", m)
	}
	// unsub
	send(c, "unsub", "event."+evID)
	readWS(t, c)
	pub("event."+evID, "seat.updated")
	pub("user."+u.UserID, "ping.after")
	if m := readWS(t, c); m["type"] != "ping.after" {
		t.Fatalf("setelah unsub tidak menerima pesan event: %v", m)
	}
}
