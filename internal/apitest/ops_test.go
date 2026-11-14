package apitest

import "testing"

func TestPauseSalesImmediateAndAudited(t *testing.T) { // M39 AC2
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Beli")
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201)

	o.C.POST(api+"/organizer/events/"+evID+"/pause-sales", M{"reason": ""}).ExpectErr(400, "VALIDATION_FAILED")
	o.C.POST(api+"/organizer/events/"+evID+"/pause-sales", M{"reason": "gangguan sistem tiket"}).Expect(200)
	u2 := e.Verified("Beli Dua")
	u2.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).ExpectErr(409, "SALE_PAUSED")
	o.C.POST(api+"/organizer/events/"+evID+"/resume-sales", nil).Expect(200)
	u2.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201)
	if e.QueryInt("SELECT count(*) FROM audit_logs WHERE action IN ('sales.pause','sales.resume')") != 2 {
		t.Fatal("jeda/lanjut harus teraudit")
	}
	// admin juga bisa; organizer lain tidak
	admin := e.UserWithRole("admin", "Admin Ops")
	admin.POST(api+"/admin/ops/events/"+evID+"/pause", M{"reason": "keputusan insiden"}).Expect(200)
	o2 := e.NewOrganizer(true)
	o2.C.POST(api+"/organizer/events/"+evID+"/resume-sales", nil).ExpectErr(404, "NOT_FOUND")
	var cr struct {
		SalesPaused bool `json:"sales_paused"`
		ActiveHolds int  `json:"active_holds"`
	}
	admin.GET(api + "/admin/ops/events/" + evID + "/control-room").Expect(200).Data(&cr)
	if !cr.SalesPaused || cr.ActiveHolds != 2 {
		t.Fatalf("control room: %+v", cr)
	}
	admin.PUT(api+"/admin/ops/events/"+evID+"/admit-rate", M{"rate": 99999}).ExpectErr(400, "VALIDATION_FAILED")
	admin.PUT(api+"/admin/ops/events/"+evID+"/admit-rate", M{"rate": 120}).Expect(204)
	if e.QueryInt("SELECT admit_rate FROM events WHERE id=$1", evID) != 120 {
		t.Fatal("admit_rate harus berubah")
	}
}

func TestMaintenanceModeBlocksPublicButNotAdminOrWebhook(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 10)
	u := e.Verified("Bayar")
	oid := e.buyPaidPendingPay(u, slug, tier)
	admin := e.UserWithRole("admin", "Admin Maint")
	admin.PUT(api+"/admin/ops/maintenance", M{"on": true, "message": ""}).ExpectErr(400, "VALIDATION_FAILED")
	admin.PUT(api+"/admin/ops/maintenance", M{"on": true, "message": "Sedang pemeliharaan database", "eta": "30 menit"}).Expect(204)
	e.Svc.Ops.ResetCache()

	r := e.Anon().GET(api + "/events")
	r.ExpectErr(503, "MAINTENANCE")
	if r.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After wajib")
	}
	// status & pengumuman tetap tersedia; admin tetap bisa; webhook bayar tetap diterima
	var st struct{ Overall string }
	e.Anon().GET(api + "/status").Expect(200).Data(&st)
	if st.Overall != "maintenance" {
		t.Fatalf("status: %s", st.Overall)
	}
	admin.GET(api + "/admin/ops/flags").Expect(200)
	ref := e.QueryStr("SELECT provider_ref FROM payments WHERE order_id=$1", oid)
	e.Anon().Do("POST", "/webhooks/midtrans", e.Gateway.Settle(ref, "gopay")).Expect(200)
	if e.QueryStr("SELECT status FROM orders WHERE id=$1", oid) != "paid" {
		t.Fatal("webhook harus tetap memproses saat maintenance")
	}
	admin.PUT(api+"/admin/ops/maintenance", M{"on": false}).Expect(204)
	e.Svc.Ops.ResetCache()
	e.Anon().GET(api + "/events").Expect(200)
}

func (e *Env) buyPaidPendingPay(u *Client, slug, tier string) string {
	e.T.Helper()
	var hold struct{ ID string }
	u.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).Expect(201).Data(&hold)
	var ord struct{ ID string }
	u.IdemPOST(api+"/orders", "pp-"+newID(), M{"hold_id": hold.ID}).Expect(201).Data(&ord)
	u.IdemPOST(api+"/orders/"+ord.ID+"/pay", "pp-pay-"+newID(), nil).Expect(200)
	return ord.ID
}

func TestFeatureFlagRolloutIsStablePerSubject(t *testing.T) {
	e := NewEnv(t)
	admin := e.UserWithRole("admin", "Admin Flag")
	admin.PUT(api+"/admin/ops/flags/Bad-Key", M{"enabled": true, "rollout_pct": 10}).ExpectErr(400, "VALIDATION_FAILED")
	admin.PUT(api+"/admin/ops/flags/fitur.baru", M{"enabled": true, "rollout_pct": 50}).Expect(200)
	e.Svc.Ops.ResetCache()
	on := 0
	for i := 0; i < 200; i++ {
		d := "dev-" + newID()
		var f map[string]bool
		e.Anon().GET(api+"/flags", "X-Device-Id", d).Expect(200).Data(&f)
		var f2 map[string]bool
		e.Anon().GET(api+"/flags", "X-Device-Id", d).Expect(200).Data(&f2)
		if f["fitur.baru"] != f2["fitur.baru"] {
			t.Fatal("hasil harus stabil per subjek")
		}
		if f["fitur.baru"] {
			on++
		}
	}
	if on < 60 || on > 140 {
		t.Fatalf("rollout 50%% harus mendekati setengah: %d/200", on)
	}
	admin.PUT(api+"/admin/ops/flags/fitur.baru", M{"enabled": false, "rollout_pct": 100}).Expect(200)
	e.Svc.Ops.ResetCache()
	var f map[string]bool
	e.Anon().GET(api + "/flags").Expect(200).Data(&f)
	if f["fitur.baru"] {
		t.Fatal("flag nonaktif harus false")
	}
}

func TestAnnouncementsScheduledAndStatusPage(t *testing.T) {
	e := NewEnv(t)
	admin := e.UserWithRole("admin", "Admin Ann")
	var a struct{ ID string }
	admin.POST(api+"/admin/ops/announcements", M{"level": "incident", "body_id": "Pembayaran QRIS sedang terganggu"}).Expect(201).Data(&a)
	admin.POST(api+"/admin/ops/announcements", M{"level": "bogus", "body_id": "x"}).ExpectErr(400, "VALIDATION_FAILED")
	var list []map[string]any
	e.Anon().GET(api + "/announcements").Expect(200).Data(&list)
	if len(list) != 1 {
		t.Fatalf("1 pengumuman aktif: %d", len(list))
	}
	var st struct {
		Overall string `json:"overall"`
	}
	e.Anon().GET(api + "/status").Expect(200).Data(&st)
	if st.Overall != "degraded" {
		t.Fatalf("insiden aktif → degraded: %s", st.Overall)
	}
	admin.POST(api+"/admin/ops/announcements/"+a.ID+"/end", nil).Expect(204)
	e.Anon().GET(api + "/announcements").Expect(200).Data(&list)
	if len(list) != 0 {
		t.Fatal("harus berakhir")
	}
}
