package apitest

import (
	"testing"
	"time"
)

func TestOrganizerAnalyticsSalesFunnelAndGateFlow(t *testing.T) {
	e := NewEnv(t)
	o, evID, slug, tier := e.publishedGA(100000, 30)
	// view + pembelian
	for i := 0; i < 3; i++ {
		e.Anon().GET(api + "/events/" + slug).Expect(200)
	}
	u1 := e.Verified("Satu")
	u2 := e.Verified("Dua")
	e.buyPaid(u1, slug, tier, 2, "gopay")
	e.buyPaid(u2, slug, tier, 1, "gopay")
	if _, err := e.Svc.Analytics.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var sales []struct {
		Orders, Tickets int
		Revenue         int64
	}
	o.C.GET(api + "/organizer/events/" + evID + "/analytics/sales?bucket=hour").Expect(200).Data(&sales)
	var orders, tickets int
	var rev int64
	for _, p := range sales {
		orders += p.Orders
		tickets += p.Tickets
		rev += p.Revenue
	}
	if orders != 2 || tickets != 3 || rev < 300000 {
		t.Fatalf("deret penjualan: orders=%d tickets=%d rev=%d (%v)", orders, tickets, rev, sales)
	}
	o.C.GET(api+"/organizer/events/"+evID+"/analytics/sales?bucket=minute").ExpectErr(400, "VALIDATION_FAILED")
	var fn []struct {
		Stage string
		Count int64
	}
	o.C.GET(api + "/organizer/events/" + evID + "/analytics/funnel").Expect(200).Data(&fn)
	got := map[string]int64{}
	for _, f := range fn {
		got[f.Stage] = f.Count
	}
	if got["view"] != 3 || got["paid"] != 2 || got["hold"] != 2 || got["ticket"] != 2 {
		t.Fatalf("funnel: %v", got)
	}
	// gate flow: 1 tiket dipindai
	tid := ticketIDs(t, u1)[0]
	var q qr
	u1.GET(api + "/tickets/" + tid + "/qr").Expect(200).Data(&q)
	o.C.POST(api+"/gate/scan", M{"payload": q.Payload, "event_id": evID}).Expect(200)
	var gf []struct{ Entered int }
	o.C.GET(api + "/organizer/events/" + evID + "/analytics/gate-flow?from=" + e.Clock.Now().Add(-time.Hour).Format(time.RFC3339) + "&to=" + e.Clock.Now().Add(time.Hour).Format(time.RFC3339)).Expect(200).Data(&gf)
	tot := 0
	for _, g := range gf {
		tot += g.Entered
	}
	if tot != 1 {
		t.Fatalf("arus masuk: %v", gf)
	}
	// organizer lain 404
	o2 := e.NewOrganizer(true)
	o2.C.GET(api+"/organizer/events/"+evID+"/analytics/funnel").ExpectErr(404, "NOT_FOUND")
}

func TestBusinessDashboardAdminOnly(t *testing.T) {
	e := NewEnv(t)
	_, _, slug, tier := e.publishedGA(100000, 30)
	u := e.Verified("Beli")
	e.buyPaid(u, slug, tier, 1, "gopay")
	if _, err := e.Svc.Analytics.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	admin := e.UserWithRole("admin", "Admin BI")
	var d struct {
		PaidOrders int `json:"paid_orders"`
		Methods    []struct {
			Method string
			Total  int
		} `json:"payment_methods"`
		Funnel []struct{ Stage string }
	}
	admin.GET(api + "/admin/analytics/business").Expect(200).Data(&d)
	if d.PaidOrders != 1 || len(d.Funnel) != 6 || len(d.Methods) == 0 {
		t.Fatalf("dasbor: %+v", d)
	}
	u.GET(api+"/admin/analytics/business").ExpectErr(403, "FORBIDDEN")
}
