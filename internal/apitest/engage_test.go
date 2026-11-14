package apitest

import (
	"strings"
	"testing"
	"time"
)

// publishedSeated: event bernomor dengan 1 tier & 1 seksi (rows × perRow).
func (e *Env) publishedSeated(rows, perRow int) (o *Org, evID, slug, tier string) {
	e.T.Helper()
	o = e.NewOrganizer(true)
	venue := e.NewVenue(o.C, "Jakarta")
	var ev struct{ ID, Slug string }
	o.C.POST(api+"/organizer/events", M{"venue_id": venue, "title": "Konser Duduk Rapi", "mode": "seated", "starts_at": e.future(30), "ends_at": e.future(31)}).Expect(201).Data(&ev)
	var tr struct{ ID string }
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", M{"name": "CAT1", "price": 400000}).Expect(201).Data(&tr)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": tr.ID, "rows": rows, "seats_per_row": perRow}}}).Expect(200)
	e.ForcePublish(ev.ID)
	e.Exec("INSERT INTO sale_phases(id,event_id,name,audience,starts_at,ends_at) VALUES ($1,$2,'Umum','public',now()-interval '1 day',now()+interval '20 days')", newID(), ev.ID)
	return o, ev.ID, ev.Slug, tr.ID
}

func TestBestAvailableContiguousNoOrphanAndAutoHold(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedSeated(3, 6)
	u := e.Verified("Rombongan")
	var sg struct {
		SeatIDs    []string `json:"seat_ids"`
		Labels     []string `json:"labels"`
		Contiguous bool
		Orphans    int
	}
	e.Anon().GET(api + "/events/" + slug + "/seats/suggest?tier_id=" + tier + "&qty=4").Expect(200).Data(&sg)
	if len(sg.SeatIDs) != 4 || !sg.Contiguous {
		t.Fatalf("4 kursi berurutan satu baris: %+v", sg)
	}
	rows := map[string]bool{}
	for _, l := range sg.Labels {
		parts := strings.Split(l, "-")
		rows[parts[0]+"-"+parts[1]] = true
	}
	if len(rows) != 1 {
		t.Fatalf("harus satu baris: %v", sg.Labels)
	}
	// anti-orphan: baris 6 kursi, minta 4 → sisa 2 (bukan 1). Minta 5 → sisa 1 di ujung: harus hindari baris itu bila ada baris lain
	e.Anon().GET(api + "/events/" + slug + "/seats/suggest?tier_id=" + tier + "&qty=5").Expect(200).Data(&sg)
	if sg.Orphans != 0 && len(sg.SeatIDs) == 5 {
		// semua baris 6 kursi → 5 selalu menyisakan 1 orphan; harus dilaporkan jujur
		if sg.Orphans != 1 {
			t.Fatalf("orphan harus dilaporkan: %+v", sg)
		}
	}
	// ambil 2 kursi tengah satu baris agar baris itu terfragmentasi, lalu minta 2: tidak boleh menyisakan orphan di baris lain bila ada jendela bersih
	e.Exec("UPDATE seats SET status='sold' WHERE event_id=(SELECT event_id FROM tiers WHERE id=$1) AND row_label='1' AND seat_no IN ('2')", tier)
	e.Anon().GET(api + "/events/" + slug + "/seats/suggest?tier_id=" + tier + "&qty=2").Expect(200).Data(&sg)
	if sg.Orphans != 0 {
		t.Fatalf("tersedia pilihan tanpa orphan: %+v", sg)
	}
	// preferensi + auto-hold satu request
	var evv struct{ ID string }
	e.Anon().GET(api + "/events/" + slug).Expect(200).Data(&evv)
	u.PUT(api+"/events/"+slug+"/preferences", M{"tiers": []string{newID()}}).ExpectErr(400, "VALIDATION_FAILED")
	u.PUT(api+"/events/"+slug+"/preferences", M{"tiers": []string{tier}}).Expect(200)
	var hold struct {
		ID    string
		Items []struct {
			SeatID *string `json:"seat_id"`
		}
	}
	u.POST(api+"/events/"+slug+"/holds/auto", M{"qty": 3}).Expect(201).Data(&hold)
	if len(hold.Items) != 3 {
		t.Fatalf("auto-hold 3 kursi: %+v", hold)
	}
	// penuh → 409
	e.Exec("UPDATE seats SET status='sold' WHERE status='available'")
	u2 := e.Verified("Telat Lagi")
	u2.POST(api+"/events/"+slug+"/holds/auto", M{"qty": 2}).Expect(409)
}

func TestFavoritesRemindersFollowsAndRecommendations(t *testing.T) {
	e := NewEnv(t)
	_, evID, _, _ := e.publishedGA(100000, 10)
	e.Exec("UPDATE events SET genre='Indie' WHERE id=$1", evID)
	u := e.Verified("Penikmat")
	u.POST(api+"/me/favorites/"+evID, nil).Expect(204)
	var fav []map[string]any
	u.GET(api + "/me/favorites").Expect(200).Data(&fav)
	if len(fav) != 1 {
		t.Fatalf("favorit: %d", len(fav))
	}
	u.DELETE(api + "/me/favorites/" + evID).Expect(204)
	u.POST(api+"/me/follows", M{"kind": "bogus", "target": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	var recs []struct {
		Reasons []string
		Event   struct{ ID string }
	}
	u.GET(api + "/me/recommendations").Expect(200).Data(&recs)
	if len(recs) != 0 {
		t.Fatal("tanpa follow tak ada rekomendasi")
	}
	u.POST(api+"/me/follows", M{"kind": "genre", "target": "indie"}).Expect(204)
	u.GET(api + "/me/recommendations").Expect(200).Data(&recs)
	if len(recs) != 1 || recs[0].Event.ID != evID || len(recs[0].Reasons) == 0 {
		t.Fatalf("rekomendasi berdasarkan genre: %+v", recs)
	}
}

func TestCalendarICSAndDayPlan(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Perencana")
	e.buyPaid(u, slug, tier, 1, "gopay")
	r := e.Anon().GET(api + "/events/" + slug + "/calendar.ics").Expect(200)
	body := string(r.Body)
	for _, want := range []string{"BEGIN:VCALENDAR", "BEGIN:VEVENT", "SUMMARY:Konser Beli", "BEGIN:VALARM", "END:VCALENDAR"} {
		if !strings.Contains(body, want) {
			t.Fatalf("ICS tanpa %q:\n%s", want, body)
		}
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/calendar") {
		t.Fatal("content-type")
	}
	mine := string(u.GET(api + "/me/calendar.ics").Expect(200).Body)
	if !strings.Contains(mine, "SUMMARY:Konser Beli") {
		t.Fatal("kalender pribadi memuat acara mendatang")
	}
	e.Exec("UPDATE events SET schedule_state='cancelled', status='cancelled' WHERE id=$1", evID)
	if strings.Contains(string(u.GET(api+"/me/calendar.ics").Expect(200).Body), "SUMMARY:Konser Beli") {
		t.Fatal("event batal tidak masuk kalender pribadi")
	}
	var plan struct {
		MapsURL string `json:"maps_url"`
		Tips    []string
	}
	u.GET(api + "/tickets/" + ticketIDs(t, u)[0] + "/plan").Expect(200).Data(&plan)
	if !strings.Contains(plan.MapsURL, "google.com/maps") || len(plan.Tips) == 0 {
		t.Fatalf("rencana: %+v", plan)
	}
}

func TestReadinessClockAndPracticeDoNotTouchProductionData(t *testing.T) {
	e := NewEnv(t)
	u := e.Verified("Siap Perang")
	var ck struct {
		ServerMS int64  `json:"server_ms"`
		Offset   *int64 `json:"offset_ms"`
	}
	e.Anon().GET(api + "/clock").Expect(200).Data(&ck)
	if ck.ServerMS == 0 || ck.Offset != nil {
		t.Fatal(ck)
	}
	e.Anon().GET(api + "/clock?client_ms=" + itoa(ck.ServerMS-2000)).Expect(200).Data(&ck)
	if ck.Offset == nil || *ck.Offset < 1900 || *ck.Offset > 2100 {
		t.Fatalf("offset ±2000: %v", ck.Offset)
	}
	var rd struct {
		Ready  bool
		Checks []struct{ Key, Status string }
	}
	u.GET(api + "/readiness?client_ms=" + itoa(ck.ServerMS)).Expect(200).Data(&rd)
	if !rd.Ready || len(rd.Checks) < 4 {
		t.Fatalf("readiness: %+v", rd)
	}
	unv := e.Register(uniqEmail("unv"), "Belum Verif")
	unv.GET(api + "/readiness").Expect(200).Data(&rd)
	if rd.Ready {
		t.Fatal("email belum terverifikasi → tidak siap")
	}

	before := e.QueryInt("SELECT (SELECT count(*) FROM holds)+(SELECT count(*) FROM orders)+(SELECT count(*) FROM tickets)")
	var st struct {
		State    string
		Position int
	}
	u.POST(api+"/practice/start", nil).Expect(200).Data(&st)
	if st.State != "waiting" || st.Position <= 0 {
		t.Fatalf("latihan menunggu: %+v", st)
	}
	u.POST(api+"/practice/hold", M{"qty": 2}).ExpectErr(409, "NOT_ADMITTED")
	e.Clock.Advance(60 * time.Second) // simulasi 120/dtk × 60 dtk ≥ antrean maksimum 4000
	u = e.Login(u.Email)
	u.GET(api + "/practice/status").Expect(200).Data(&st)
	if st.State != "admitted" {
		t.Fatalf("setelah jeda harus admitted: %+v", st)
	}
	u.POST(api+"/practice/hold", M{"qty": 2}).Expect(200)
	u.POST(api+"/practice/pay", nil).Expect(200).Data(&st)
	if st.State != "done" {
		t.Fatalf("selesai: %+v", st)
	}
	after := e.QueryInt("SELECT (SELECT count(*) FROM holds)+(SELECT count(*) FROM orders)+(SELECT count(*) FROM tickets)")
	if before != after {
		t.Fatal("latihan tidak boleh menyentuh data produksi")
	}
}

func itoa(n int64) string { return fmtInt(n) }

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
