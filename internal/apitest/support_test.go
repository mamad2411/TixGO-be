package apitest

import (
	"strings"
	"testing"
	"time"
)

func TestSupportTicketFlowAndAgentQueue(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Penanya")
	other := e.Verified("Lain")
	oid := e.buyPaid(u, slug, tier, 1, "gopay")
	agent := e.UserWithRole("support", "Agen Satu")

	// konteks wajib milik sendiri
	other.POST(api+"/support/tickets", M{"category": "refund", "subject": "Mau refund pesanan", "body": "Tolong proses refund ya.", "order_id": oid}).ExpectErr(404, "NOT_FOUND")
	var tk struct {
		ID       string
		Number   int64
		Priority string
	}
	u.POST(api+"/support/tickets", M{"category": "refund", "subject": "Mau refund pesanan", "body": "Tolong proses refund ya.", "order_id": oid}).Expect(201).Data(&tk)
	if tk.Number == 0 || tk.Priority != "normal" {
		t.Fatalf("tiket: %+v", tk)
	}
	if m, ok := e.Mail.LastMailTo(u.Email); !ok || !strings.Contains(m.Subject, "Permintaan bantuan kami terima") {
		t.Fatal("email konfirmasi")
	}
	// pelanggan lain tak bisa membaca; pelanggan tak bisa memakai API agen
	other.GET(api+"/support/tickets/"+tk.ID).ExpectErr(404, "NOT_FOUND")
	u.GET(api+"/agent/support/tickets").ExpectErr(403, "FORBIDDEN")

	// agen: lihat dengan ringkasan order (tanpa email pembeli di ringkasan), catatan internal tak terlihat pelanggan
	var th struct {
		Order    map[string]any
		Messages []map[string]any
	}
	agent.GET(api + "/agent/support/tickets/" + tk.ID).Expect(200).Data(&th)
	if th.Order == nil || th.Order["event_title"] == nil {
		t.Fatalf("ringkasan order untuk agen: %v", th.Order)
	}
	agent.POST(api+"/agent/support/tickets/"+tk.ID+"/messages", M{"body": "catatan: cek kebijakan non-refundable", "internal": true}).Expect(204)
	agent.POST(api+"/agent/support/tickets/"+tk.ID+"/messages", M{"body": "Halo, kebijakan event ini non-refundable."}).Expect(204)
	var mine struct {
		Ticket   struct{ Status string }
		Messages []map[string]any
	}
	u.GET(api + "/support/tickets/" + tk.ID).Expect(200).Data(&mine)
	if mine.Ticket.Status != "pending_customer" || len(mine.Messages) != 2 {
		t.Fatalf("pelanggan melihat 2 pesan publik (bukan catatan internal): %+v", mine)
	}
	if mine.Messages[1]["author"] != "Tim TixGo" {
		t.Fatal("nama agen disamarkan sebagai Tim TixGo")
	}
	if e.QueryInt("SELECT count(*) FROM support_tickets WHERE first_response_at IS NOT NULL") != 1 {
		t.Fatal("first_response_at terisi")
	}
	if m, _ := e.Mail.LastMailTo(u.Email); !strings.Contains(m.Subject, "Balasan bantuan") {
		t.Fatal("email balasan")
	}
	// balasan pelanggan → open; resolve; buka kembali dalam 14 hari; tutup otomatis
	u.POST(api+"/support/tickets/"+tk.ID+"/messages", M{"body": "Baik, terima kasih infonya."}).Expect(204)
	agent.POST(api+"/agent/support/tickets/"+tk.ID+"/resolve", nil).Expect(204)
	u.POST(api+"/support/tickets/"+tk.ID+"/messages", M{"body": "Tunggu, saya masih ada pertanyaan."}).Expect(204)
	if e.QueryStr("SELECT status FROM support_tickets") != "open" {
		t.Fatal("dibuka kembali")
	}
	agent.POST(api+"/agent/support/tickets/"+tk.ID+"/resolve", nil).Expect(204)
	e.Clock.Advance(15 * 24 * time.Hour)
	if _, err := e.Svc.Support.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	u = e.Login(u.Email)
	if e.QueryStr("SELECT status FROM support_tickets") != "closed" {
		t.Fatal("resolved >14 hari → closed")
	}
	u.POST(api+"/support/tickets/"+tk.ID+"/messages", M{"body": "Halo lagi, masih ada?"}).ExpectErr(409, "INVALID_STATE")
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action='support.view'") == 0 {
		t.Fatal("akses data oleh agen teraudit")
	}
	_ = evID
}

func TestSupportPriorityEscalation(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Darurat")
	e.buyPaid(u, slug, tier, 1, "gopay")
	// hari-H (event mulai < 24 jam) → prioritas tinggi
	e.Exec("UPDATE events SET starts_at=now()+interval '5 hours', ends_at=now()+interval '10 hours' WHERE id=$1", evID)
	var a struct{ Priority string }
	u.POST(api+"/support/tickets", M{"category": "ticket", "subject": "QR tidak muncul", "body": "Tiket saya tidak tampil QR nya.", "event_id": evID}).Expect(201).Data(&a)
	if a.Priority != "high" {
		t.Fatalf("hari-H harus high: %s", a.Priority)
	}
	// tiket pembayaran >24 jam otomatis eskalasi
	var b struct{ ID, Priority string }
	u.POST(api+"/support/tickets", M{"category": "payment", "subject": "Sudah bayar belum terbit", "body": "Saya sudah bayar tapi belum ada tiket."}).Expect(201).Data(&b)
	if b.Priority != "normal" {
		t.Fatal("awal normal")
	}
	e.Clock.Advance(25 * time.Hour)
	e.Exec("UPDATE support_tickets SET created_at = created_at - interval '25 hours' WHERE id=$1", b.ID)
	if _, err := e.Svc.Support.Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.QueryStr("SELECT priority FROM support_tickets WHERE id=$1", b.ID) != "high" {
		t.Fatal("pembayaran >24 jam → high")
	}
}
