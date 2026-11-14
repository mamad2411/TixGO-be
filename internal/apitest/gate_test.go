package apitest

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type qr struct {
	Payload string `json:"payload"`
}

func ticketIDs(t *testing.T, u *Client) []string {
	t.Helper()
	var l []struct {
		ID string `json:"id"`
	}
	u.GET(api + "/tickets").Expect(200).Data(&l)
	ids := make([]string, len(l))
	for i, x := range l {
		ids[i] = x.ID
	}
	return ids
}

func TestGateScanOKThenDuplicate(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Penonton")
	e.buyPaid(u, slug, tier, 1, "gopay")
	tid := ticketIDs(t, u)[0]
	var q qr
	u.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&q)

	var r struct {
		Result string `json:"result"`
	}
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID, "scan_id": "scan-0001-a"}).Expect(200).Data(&r)
	if r.Result != "OK" {
		t.Fatalf("scan pertama: %s", r.Result)
	}
	// idempoten: scan_id sama → hasil sama
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID, "scan_id": "scan-0001-a"}).Expect(200).Data(&r)
	if r.Result != "OK" {
		t.Fatalf("scan_id sama harus hasil sama: %s", r.Result)
	}
	// scan baru → DUPLICATE
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID, "scan_id": "scan-0002-b"}).Expect(200).Data(&r)
	if r.Result != "DUPLICATE" {
		t.Fatalf("scan kedua: %s", r.Result)
	}
	if n := e.QueryInt("SELECT count(*) FROM gate_scans WHERE result='OK'"); n != 1 {
		t.Fatalf("OK harus tepat 1: %d", n)
	}
	var sum struct {
		Entered, Remaining int
	}
	o.C.GET(api + "/gate/events/" + evID + "/summary").Expect(200).Data(&sum)
	if sum.Entered != 1 || sum.Remaining != 0 {
		t.Fatalf("ringkasan: %+v", sum)
	}
}

func TestGateConcurrentScansOnlyOneOK(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Rebut")
	e.buyPaid(u, slug, tier, 1, "gopay")
	tid := ticketIDs(t, u)[0]
	var q qr
	u.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&q)
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var r struct {
				Result string `json:"result"`
			}
			o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID, "scan_id": "conc-scan-" + string(rune('a'+i))}).Expect(200).Data(&r)
			if r.Result == "OK" {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("hanya 1 OK: %d", ok.Load())
	}
}

func TestGateRejectsExpiredCodeWrongEventVoidAndOutsiders(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	_, evB, _, _ := e.publishedGA(50000, 5)
	u := e.Verified("Tolak")
	e.buyPaid(u, slug, tier, 2, "gopay")
	ids := ticketIDs(t, u)
	var q1, q2 qr
	u.GET(api + "/tickets/" + ids[0] + "/qr").Expect(200).Data(&q1)
	u.GET(api + "/tickets/" + ids[1] + "/qr").Expect(200).Data(&q2)
	var r struct {
		Result string `json:"result"`
	}
	// kode basi (jauh melewati toleransi ±1 langkah)
	e.Clock.Advance(10 * time.Minute)
	o.C.POST(api+"/gate/scan", M{"payload": q1.Payload, "event_id": evID}).Expect(200).Data(&r)
	if r.Result != "EXPIRED_CODE" {
		t.Fatalf("kode basi: %s", r.Result)
	}
	// payload sampah
	o.C.POST(api+"/gate/scan", M{"payload": "sampah-bukan-qr", "event_id": evID}).Expect(200).Data(&r)
	if r.Result != "INVALID" {
		t.Fatalf("sampah: %s", r.Result)
	}
	// tiket event lain: dipindai oleh staf event lain (organizer B) pada event B → WRONG_EVENT
	ob := e.NewOrganizer(true)
	_ = ob
	u.GET(api + "/tickets/" + ids[1] + "/qr").Expect(200).Data(&q2)
	e.Exec("UPDATE events SET organizer_id=(SELECT organizer_id FROM events WHERE id=$1) WHERE id=$2", evID, evB)
	o.C.POST(api+"/gate/scan", M{"payload": q2.Payload, "event_id": evB}).Expect(200).Data(&r)
	if r.Result != "WRONG_EVENT" {
		t.Fatalf("event lain: %s", r.Result)
	}
	// tiket void
	e.Exec("UPDATE tickets SET status='void' WHERE id=$1", ids[1])
	o.C.POST(api+"/gate/scan", M{"payload": q2.Payload, "event_id": evID}).Expect(200).Data(&r)
	if r.Result != "VOID" {
		t.Fatalf("void: %s", r.Result)
	}
	// orang luar: 404, tidak membocorkan
	u.POST(api+"/gate/scan", M{"payload": q1.Payload, "event_id": evID}).ExpectErr(404, "NOT_FOUND")
}

func TestGateOfflineSyncEarliestScanWins(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Offline")
	e.buyPaid(u, slug, tier, 1, "gopay")
	tid := ticketIDs(t, u)[0]
	var m struct {
		Tickets []struct{ ID, Secret string } `json:"tickets"`
	}
	o.C.GET(api + "/gate/events/" + evID + "/manifest").Expect(200).Data(&m)
	if len(m.Tickets) != 1 || m.Tickets[0].Secret == "" {
		t.Fatalf("manifest: %+v", m)
	}
	t0 := e.Clock.Now().Add(-10 * time.Minute)
	later := t0.Add(5 * time.Minute)
	// perangkat B (lebih lambat sync) menyerahkan scan LEBIH AWAL setelah perangkat A
	var sr struct{ Accepted, Conflicts int }
	o.C.POST(api+"/gate/events/"+evID+"/sync", M{"scans": []M{{"scan_id": "dev-a-0001", "ticket_id": tid, "scanned_at": later}}}).Expect(200).Data(&sr)
	if sr.Accepted != 1 {
		t.Fatalf("A: %+v", sr)
	}
	o.C.POST(api+"/gate/events/"+evID+"/sync", M{"scans": []M{{"scan_id": "dev-b-0001", "ticket_id": tid, "scanned_at": t0}}}).Expect(200).Data(&sr)
	if sr.Accepted != 1 {
		t.Fatalf("B (lebih awal) harus menang: %+v", sr)
	}
	if e.QueryStr("SELECT scan_id FROM gate_scans WHERE result='OK'") != "dev-b-0001" {
		t.Fatal("pemenang harus scan terawal")
	}
	if e.QueryStr("SELECT result FROM gate_scans WHERE scan_id='dev-a-0001'") != "DUPLICATE_OFFLINE" {
		t.Fatal("yang lebih akhir harus DUPLICATE_OFFLINE")
	}
	// kirim ulang → idempoten
	var again struct{ Duplicate int }
	o.C.POST(api+"/gate/events/"+evID+"/sync", M{"scans": []M{{"scan_id": "dev-b-0001", "ticket_id": tid, "scanned_at": t0}}}).Expect(200).Data(&again)
	if again.Duplicate != 1 {
		t.Fatalf("idempoten: %+v", again)
	}
}
