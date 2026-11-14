package apitest

import (
	"testing"
	"time"
)

func (e *Env) seedBank(orgID string) {
	e.T.Helper()
	enc, err := e.Svc.Box.Encrypt([]byte("1234567890"))
	if err != nil {
		e.T.Fatal(err)
	}
	e.Exec(`INSERT INTO bank_accounts (id, organizer_id, bank_code, account_no_enc, account_no_last4, account_name, verified_at, active_from) VALUES ($1,$2,'BCA',$3,'7890','PT Panggung',now(),now()-interval '30 days')`, newID(), orgID, enc)
}

func TestSettlementReleaseAndPayoutFlow(t *testing.T) { // M34
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(200000, 50)
	e.seedBank(o.ID)
	u := e.Verified("Pembeli")
	e.buyPaid(u, slug, tier, 2, "gopay")
	e.ledgerBalanced()

	var bal struct {
		Pending, Available, Paid int64
		InTransit                int64 `json:"in_transit"`
	}
	o.C.GET(api + "/organizer/finance/balance").Expect(200).Data(&bal)
	if bal.Pending <= 0 || bal.Available != 0 {
		t.Fatalf("setelah jual: pending>0, available=0: %+v", bal)
	}
	// belum bisa cair
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": 100000}, "Idempotency-Key", "payout-key-01").ExpectErr(422, "VALIDATION_FAILED")

	// event selesai + masa tahan lewat → rilis
	e.Exec("UPDATE events SET status='finished', starts_at=now()-interval '10 days', ends_at=now()-interval '9 days' WHERE id=$1", evID)
	n, err := e.Svc.Finance.ReleaseSettlements(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("rilis: n=%d err=%v", n, err)
	}
	if n, _ := e.Svc.Finance.ReleaseSettlements(t.Context()); n != 0 {
		t.Fatal("rilis harus sekali per event")
	}
	o.C.GET(api + "/organizer/finance/balance").Expect(200).Data(&bal)
	if bal.Pending != 0 || bal.Available <= 0 {
		t.Fatalf("setelah rilis: %+v", bal)
	}
	avail := bal.Available
	e.ledgerBalanced()

	// minta pencairan melebihi saldo & di bawah minimum
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": avail + 1}, "Idempotency-Key", "payout-key-02").ExpectErr(422, "VALIDATION_FAILED")
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": 1000}, "Idempotency-Key", "payout-key-03").ExpectErr(400, "VALIDATION_FAILED")
	var po struct {
		ID, Status string
	}
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": avail}, "Idempotency-Key", "payout-key-04").Expect(201).Data(&po)
	o.C.GET(api + "/organizer/finance/balance").Expect(200).Data(&bal)
	if bal.Available != 0 || bal.InTransit != avail {
		t.Fatalf("saldo dicadangkan: %+v", bal)
	}

	fin := e.UserWithRole("finance", "Finance Satu")
	fin2 := e.UserWithRole("finance", "Finance Dua")
	// organizer tak boleh akses antrean finance
	o.C.GET(api+"/admin/finance/payouts").ExpectErr(403, "FORBIDDEN")
	var list []map[string]any
	fin.GET(api + "/admin/finance/payouts?status=requested").Expect(200).Data(&list)
	if len(list) != 1 || list[0]["account_no"] != "1234567890" {
		t.Fatalf("finance melihat rekening tujuan: %v", list)
	}
	// mark-paid sebelum approve ditolak
	fin.Upload(api+"/admin/finance/payouts/"+po.ID+"/mark-paid", nil, "proof", "b.png", pngBytes(8, 8)).ExpectErr(409, "INVALID_STATE")
	// pemohon = penyetuju ditolak
	e.Exec("UPDATE payouts SET requested_by=$2 WHERE id=$1", po.ID, fin.UserID)
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/approve", nil).ExpectErr(403, "FORBIDDEN")
	e.Exec("UPDATE payouts SET requested_by=(SELECT owner_user_id FROM organizers WHERE id=$2) WHERE id=$1", po.ID, o.ID)
	fin2.POST(api+"/admin/finance/payouts/"+po.ID+"/approve", nil).Expect(200)
	fin2.POST(api+"/admin/finance/payouts/"+po.ID+"/process", nil).Expect(200)
	// tanpa bukti ditolak
	fin2.Upload(api+"/admin/finance/payouts/"+po.ID+"/mark-paid", nil, "lain", "x.txt", []byte("x")).ExpectErr(400, "VALIDATION_FAILED")
	fin2.Upload(api+"/admin/finance/payouts/"+po.ID+"/mark-paid", map[string]string{"note": "TF 1"}, "proof", "b.png", pngBytes(8, 8)).Expect(200)
	if s := e.QueryStr("SELECT status FROM payouts WHERE id=$1", po.ID); s != "paid" {
		t.Fatalf("status: %s", s)
	}
	o.C.GET(api + "/organizer/finance/balance").Expect(200).Data(&bal)
	if bal.InTransit != 0 || bal.Paid != avail {
		t.Fatalf("setelah dibayar: %+v", bal)
	}
	e.ledgerBalanced()
	var h struct{ Balanced bool }
	fin.GET(api + "/admin/finance/ledger-health").Expect(200).Data(&h)
	if !h.Balanced {
		t.Fatal("ledger-health harus seimbang")
	}
}

func TestPayoutRejectAndFailReturnBalance(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(300000, 50)
	e.seedBank(o.ID)
	u := e.Verified("Beli")
	e.buyPaid(u, slug, tier, 1, "gopay")
	e.Exec("UPDATE events SET status='finished', starts_at=now()-interval '10 days', ends_at=now()-interval '9 days' WHERE id=$1", evID)
	_, _ = e.Svc.Finance.ReleaseSettlements(t.Context())
	var bal struct{ Available int64 }
	o.C.GET(api + "/organizer/finance/balance").Data(&bal)
	total := bal.Available
	fin := e.UserWithRole("finance", "Finance")
	var po struct{ ID string }
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": total}, "Idempotency-Key", "payout-rej-01").Expect(201).Data(&po)
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/reject", M{"note": ""}).ExpectErr(400, "VALIDATION_FAILED")
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/reject", M{"note": "dokumen belum lengkap"}).Expect(200)
	o.C.GET(api + "/organizer/finance/balance").Data(&bal)
	if bal.Available != total {
		t.Fatalf("saldo kembali setelah ditolak: %d vs %d", bal.Available, total)
	}
	// gagal transfer → kembali, lalu coba ulang
	o.C.POST("/api/v1/organizer/finance/payouts", M{"amount": total}, "Idempotency-Key", "payout-rej-02").Expect(201).Data(&po)
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/approve", nil).Expect(200)
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/process", nil).Expect(200)
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/fail", M{"note": "rekening salah"}).Expect(200)
	o.C.GET(api + "/organizer/finance/balance").Data(&bal)
	if bal.Available != total {
		t.Fatalf("saldo kembali setelah gagal: %d", bal.Available)
	}
	fin.POST(api+"/admin/finance/payouts/"+po.ID+"/process", nil).Expect(200) // retry
	o.C.GET(api + "/organizer/finance/balance").Data(&bal)
	if bal.Available != 0 {
		t.Fatalf("retry mencadangkan saldo lagi: %d", bal.Available)
	}
	e.ledgerBalanced()
}

func TestEventSettlementReportAndXLSX(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 50)
	u := e.Verified("Lap")
	e.buyPaid(u, slug, tier, 3, "gopay")
	var rep struct {
		Orders int
		Gross  int64
		Tiers  []struct {
			Name string
			Qty  int
		}
	}
	o.C.GET(api + "/organizer/finance/events/" + evID + "/settlement").Expect(200).Data(&rep)
	if rep.Orders != 1 || rep.Gross != 300000 || len(rep.Tiers) != 1 || rep.Tiers[0].Qty != 3 {
		t.Fatalf("laporan: %+v", rep)
	}
	r := o.C.GET(api + "/organizer/finance/events/" + evID + "/settlement.xlsx").Expect(200)
	if len(r.Body) < 500 || string(r.Body[:2]) != "PK" {
		t.Fatalf("xlsx tidak valid (%d byte)", len(r.Body))
	}
	// organizer lain 404
	o2 := e.NewOrganizer(true)
	o2.C.GET(api+"/organizer/finance/events/"+evID+"/settlement").ExpectErr(404, "NOT_FOUND")
	_ = time.Now
}

func TestReconcileGatewayReportCreatesExceptions(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 50)
	u := e.Verified("Rec")
	e.buyPaid(u, slug, tier, 1, "gopay")
	ref := e.QueryStr("SELECT provider_ref FROM payments LIMIT 1")
	fin := e.UserWithRole("finance", "Finance")
	from := time.Now().Add(-48 * time.Hour)
	to := time.Now().Add(48 * time.Hour)
	var res struct{ Matched, Exceptions int }
	fin.POST(api+"/admin/finance/recon/import", M{"from": from, "to": to, "rows": []M{{"provider_ref": ref, "amount": 100000, "status": "settlement"}}}).Expect(200).Data(&res)
	if res.Matched != 1 || res.Exceptions != 0 {
		t.Fatalf("cocok: %+v", res)
	}
	// selisih nominal + transaksi tak dikenal + hilang dari laporan
	fin.POST(api+"/admin/finance/recon/import", M{"from": from, "to": to, "rows": []M{{"provider_ref": ref, "amount": 90000, "status": "settlement"}, {"provider_ref": "ORD-TAK-DIKENAL", "amount": 5000, "status": "settlement"}}}).Expect(200).Data(&res)
	if res.Exceptions != 2 {
		t.Fatalf("2 pengecualian: %+v", res)
	}
	fin.POST(api+"/admin/finance/recon/import", M{"from": from, "to": to, "rows": []M{}}).ExpectErr(400, "VALIDATION_FAILED")
	// impor ulang tidak menggandakan pengecualian untuk payment yang sama
	fin.POST(api+"/admin/finance/recon/import", M{"from": from, "to": to, "rows": []M{{"provider_ref": ref, "amount": 90000, "status": "settlement"}}}).Expect(200)
	if n := e.QueryInt("SELECT count(*) FROM recon_exceptions WHERE kind='amount_mismatch'"); n != 1 {
		t.Fatalf("tidak boleh ganda: %d", n)
	}
	id := e.QueryStr("SELECT id FROM recon_exceptions WHERE kind='amount_mismatch'")
	fin.POST(api+"/admin/finance/recon/"+id+"/resolve", M{"status": "resolved"}).ExpectErr(400, "VALIDATION_FAILED")
	fin.POST(api+"/admin/finance/recon/"+id+"/resolve", M{"status": "resolved", "resolution": "salah input laporan"}).Expect(204)
}

func TestChargebackHoldsTicketsEvidenceAndResolution(t *testing.T) { // M47 AC3
	for _, outcome := range []string{"won", "lost"} {
		outcome := outcome
		t.Run(outcome, func(t *testing.T) {
			e := NewEnv(t)
			_, _, slug, tier := e.publishedGA(100000, 10)
			u := e.Verified("Pemegang Kartu")
			oid := e.buyPaid(u, slug, tier, 2, "credit_card")
			payID := e.QueryStr("SELECT id FROM payments WHERE order_id=$1", oid)
			fin := e.UserWithRole("finance", "Finance Sengketa")
			base := api + "/admin/finance/disputes"
			fin.POST(base, M{"payment_id": payID, "reason": "pemegang kartu menyangkal transaksi", "due_at": e.Clock.Now().Add(-time.Hour)}).ExpectErr(400, "VALIDATION_FAILED")
			var d struct{ ID string }
			fin.POST(base, M{"payment_id": payID, "reason": "pemegang kartu menyangkal transaksi", "due_at": e.Clock.Now().Add(10 * 24 * time.Hour)}).Expect(201).Data(&d)
			fin.POST(base, M{"payment_id": payID, "reason": "dobel", "due_at": e.Clock.Now().Add(24 * time.Hour)}).ExpectErr(409, "CONFLICT")
			if e.QueryInt("SELECT count(*) FROM tickets WHERE status='void'") != 2 {
				t.Fatal("tiket ditahan selama sengketa")
			}
			// QR tiket yang ditahan tidak lolos gate
			// QR tidak diterbitkan untuk tiket yang ditahan
			u.GET(api+"/tickets/"+ticketIDs(t, u)[0]+"/qr").ExpectErr(409, "TICKET_INVALID")
			fin.Upload(base+"/"+d.ID+"/evidence", map[string]string{"name": "bukti-pengiriman"}, "file", "bukti.pdf", []byte("%PDF-1.4 bukti")).Expect(204)
			fin.POST(api+"/admin/finance/recon/"+d.ID+"/resolve", M{"status": "resolved", "resolution": "paksa"}).ExpectErr(409, "INVALID_STATE")
			fin.POST(base+"/"+d.ID+"/resolve", M{"outcome": "seri"}).ExpectErr(400, "VALIDATION_FAILED")
			fin.POST(base+"/"+d.ID+"/resolve", M{"outcome": outcome, "note": "keputusan bank"}).Expect(204)
			fin.POST(base+"/"+d.ID+"/resolve", M{"outcome": outcome}).ExpectErr(409, "INVALID_STATE")
			switch outcome {
			case "won":
				if e.QueryInt("SELECT count(*) FROM tickets WHERE status='valid'") != 2 || e.QueryStr("SELECT status FROM orders WHERE id=$1", oid) != "paid" {
					t.Fatal("menang → tiket pulih, order tetap paid")
				}
			case "lost":
				if e.QueryInt("SELECT count(*) FROM tickets WHERE status='void'") != 2 || e.QueryStr("SELECT status FROM orders WHERE id=$1", oid) != "refunded" {
					t.Fatal("kalah → tiket tetap batal, order refunded")
				}
			}
			e.ledgerBalanced()
		})
	}
}
