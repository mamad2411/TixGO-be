package service

import (
	"tixgo/backend/internal/repository/sqlcgen"
)

// FeeConfig = konfigurasi biaya layanan event (docs/17 F1 Harga Jujur). Pajak belum dihitung (Q13).
type FeeConfig struct {
	BP    int
	Fixed int64
	Payer string // buyer|organizer
}

func feeConfigOf(ev sqlcgen.Event) FeeConfig {
	return FeeConfig{BP: ev.ServiceFeeBp, Fixed: ev.ServiceFeeFixed, Payer: ev.FeePayer}
}

// PerTicket menghitung biaya layanan SATU tiket: persen (dibulatkan half-up ke rupiah) + tetap.
// Dibulatkan per tiket lalu dijumlah → deterministik dan tidak bergantung urutan.
func (c FeeConfig) PerTicket(price int64) int64 {
	if price <= 0 {
		return 0 // tiket gratis tidak dikenai biaya
	}
	pct := (price*int64(c.BP) + 5000) / 10000
	return pct + c.Fixed
}

// BuyerFee = biaya yang ditambahkan ke total pembeli (0 bila ditanggung organizer).
func (c FeeConfig) BuyerFee(price int64) int64 {
	if c.Payer == "organizer" {
		return 0
	}
	return c.PerTicket(price)
}

// FinalPrice = harga akhir per tiket yang dilihat pembeli (harga + biaya layanan bila pembeli yang menanggung).
func (c FeeConfig) FinalPrice(price int64) int64 { return price + c.BuyerFee(price) }

// ---- perhitungan kutipan (pure, dapat diuji) ----

type PriceLine struct {
	TierID string
	Name   string
	Unit   int64
	Qty    int
}

type AddonLine struct {
	AddonID string
	Name    string
	Unit    int64
	Qty     int
}

type QuoteLine struct {
	TierID   string `json:"tier_id"`
	Name     string `json:"name"`
	Qty      int    `json:"qty"`
	Unit     int64  `json:"unit_price"`
	UnitFee  int64  `json:"unit_fee"`
	Subtotal int64  `json:"subtotal"`
}

type QuoteAddon struct {
	AddonID  string `json:"addon_id"`
	Name     string `json:"name"`
	Qty      int    `json:"qty"`
	Unit     int64  `json:"unit_price"`
	Subtotal int64  `json:"subtotal"`
}

// Quote = rincian harga lengkap yang ditampilkan (F1 "Harga Jujur") dan disimpan di order.
type Quote struct {
	Lines       []QuoteLine  `json:"lines"`
	Addons      []QuoteAddon `json:"addons"`
	Subtotal    int64        `json:"subtotal"`
	Discount    int64        `json:"discount"`
	AddonsTotal int64        `json:"addons_total"`
	Fee         int64        `json:"fee"`          // biaya layanan yang ditanggung pembeli
	PlatformFee int64        `json:"platform_fee"` // biaya layanan platform total (buyer atau organizer)
	Tax         int64        `json:"tax"`          // belum didukung (Q13) → selalu 0
	Total       int64        `json:"total"`
	Currency    string       `json:"currency"`
	PromoCode   *string      `json:"promo_code,omitempty"`
	FeePayer    string       `json:"fee_payer"`
}

// ComputeQuote: total = subtotal − diskon + add-on + biaya layanan(pembeli). Tidak pernah negatif.
func ComputeQuote(cfg FeeConfig, lines []PriceLine, addons []AddonLine, discount int64) Quote {
	q := Quote{Currency: "IDR", FeePayer: cfg.Payer, Lines: make([]QuoteLine, 0, len(lines)), Addons: make([]QuoteAddon, 0, len(addons))}
	for _, l := range lines {
		perFee := cfg.PerTicket(l.Unit)
		buyerFee := cfg.BuyerFee(l.Unit)
		sub := l.Unit * int64(l.Qty)
		q.Lines = append(q.Lines, QuoteLine{TierID: l.TierID, Name: l.Name, Qty: l.Qty, Unit: l.Unit, UnitFee: buyerFee, Subtotal: sub})
		q.Subtotal += sub
		q.Fee += buyerFee * int64(l.Qty)
		q.PlatformFee += perFee * int64(l.Qty)
	}
	for _, a := range addons {
		sub := a.Unit * int64(a.Qty)
		q.Addons = append(q.Addons, QuoteAddon{AddonID: a.AddonID, Name: a.Name, Qty: a.Qty, Unit: a.Unit, Subtotal: sub})
		q.AddonsTotal += sub
	}
	if discount > q.Subtotal {
		discount = q.Subtotal
	}
	if discount < 0 {
		discount = 0
	}
	q.Discount = discount
	q.Total = q.Subtotal - q.Discount + q.AddonsTotal + q.Fee + q.Tax
	if q.Total < 0 {
		q.Total = 0
	}
	return q
}

// PromoDiscount menghitung potongan dari kode promo atas baris tiket yang memenuhi syarat tier.
// Persen dibulatkan half-up ke rupiah; nominal dibatasi subtotal yang memenuhi syarat.
func PromoDiscount(p sqlcgen.PromoCode, lines []PriceLine) int64 {
	var eligible int64
	for _, l := range lines {
		if len(p.TierIds) > 0 && !containsStr(p.TierIds, l.TierID) {
			continue
		}
		eligible += l.Unit * int64(l.Qty)
	}
	if eligible <= 0 || eligible < p.MinTotal {
		return 0
	}
	var d int64
	switch p.Kind {
	case "percent":
		d = (eligible*p.Value + 50) / 100
	case "fixed":
		d = p.Value
	}
	if d > eligible {
		d = eligible
	}
	return d
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
