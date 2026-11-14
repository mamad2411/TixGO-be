package service

import (
	"testing"

	"tixgo/backend/internal/repository/sqlcgen"
)

func TestFeePerTicketRoundsHalfUpAndSkipsFree(t *testing.T) {
	c := FeeConfig{BP: 500, Fixed: 2500, Payer: "buyer"} // 5% + Rp2.500
	cases := map[int64]int64{300000: 17500, 100: 2505, 0: 0, 10: 2501}
	for price, want := range cases {
		if got := c.PerTicket(price); got != want {
			t.Errorf("PerTicket(%d)=%d, mau %d", price, got, want)
		}
	}
	// 0,5% dari 1.001 = 5,005 → 5 ; dari 1.100 = 5,5 → 6 (half-up)
	h := FeeConfig{BP: 50}
	if h.PerTicket(1100) != 6 || h.PerTicket(1001) != 5 {
		t.Errorf("pembulatan half-up salah: %d %d", h.PerTicket(1100), h.PerTicket(1001))
	}
	org := FeeConfig{BP: 500, Fixed: 2500, Payer: "organizer"}
	if org.BuyerFee(300000) != 0 || org.FinalPrice(300000) != 300000 || org.PerTicket(300000) != 17500 {
		t.Error("fee_payer=organizer: pembeli tidak membayar, platform tetap menghitung")
	}
}

func TestComputeQuoteInvariants(t *testing.T) {
	cfg := FeeConfig{BP: 1000, Fixed: 0, Payer: "buyer"}
	lines := []PriceLine{{TierID: "a", Name: "VIP", Unit: 500000, Qty: 2}, {TierID: "b", Name: "CAT", Unit: 150000, Qty: 3}}
	addons := []AddonLine{{AddonID: "p", Name: "Parkir", Unit: 20000, Qty: 2}}
	q := ComputeQuote(cfg, lines, addons, 100000)
	if q.Subtotal != 1450000 || q.Fee != 145000 || q.AddonsTotal != 40000 || q.Discount != 100000 {
		t.Fatalf("komponen salah: %+v", q)
	}
	if q.Total != q.Subtotal-q.Discount+q.AddonsTotal+q.Fee || q.Total != 1535000 {
		t.Fatalf("total salah: %d", q.Total)
	}
	// diskon melebihi subtotal → dibatasi, total tak negatif
	q = ComputeQuote(cfg, lines, nil, 99_999_999)
	if q.Discount != q.Subtotal || q.Total != q.Fee {
		t.Fatalf("diskon harus dibatasi subtotal: %+v", q)
	}
	// tiket gratis: total 0, tanpa biaya
	q = ComputeQuote(cfg, []PriceLine{{TierID: "f", Unit: 0, Qty: 4}}, nil, 0)
	if q.Total != 0 || q.Fee != 0 || q.PlatformFee != 0 {
		t.Fatalf("tiket gratis: %+v", q)
	}
	// fee organizer: pembeli tidak dikenai, platform_fee tetap tercatat
	q = ComputeQuote(FeeConfig{BP: 1000, Payer: "organizer"}, lines, nil, 0)
	if q.Fee != 0 || q.PlatformFee != 145000 || q.Total != q.Subtotal {
		t.Fatalf("fee organizer: %+v", q)
	}
}

func TestPromoDiscount(t *testing.T) {
	lines := []PriceLine{{TierID: "vip", Unit: 500000, Qty: 1}, {TierID: "cat", Unit: 100000, Qty: 2}}
	pct := sqlcgen.PromoCode{Kind: "percent", Value: 10}
	if d := PromoDiscount(pct, lines); d != 70000 {
		t.Errorf("10%% dari 700.000 = 70.000, dapat %d", d)
	}
	onlyCat := sqlcgen.PromoCode{Kind: "percent", Value: 50, TierIds: []string{"cat"}}
	if d := PromoDiscount(onlyCat, lines); d != 100000 {
		t.Errorf("50%% dari tier cat saja = 100.000, dapat %d", d)
	}
	fixed := sqlcgen.PromoCode{Kind: "fixed", Value: 1_000_000}
	if d := PromoDiscount(fixed, lines); d != 700000 {
		t.Errorf("fixed dibatasi subtotal: %d", d)
	}
	minTotal := sqlcgen.PromoCode{Kind: "fixed", Value: 5000, MinTotal: 800000}
	if d := PromoDiscount(minTotal, lines); d != 0 {
		t.Errorf("di bawah min_total = 0: %d", d)
	}
	// pembulatan half-up: 15% dari 1.010 = 151,5 → 152
	if d := PromoDiscount(sqlcgen.PromoCode{Kind: "percent", Value: 15}, []PriceLine{{Unit: 1010, Qty: 1}}); d != 152 {
		t.Errorf("half-up: %d", d)
	}
}
