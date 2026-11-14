package service

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/notify"
	"tixgo/backend/internal/repository/sqlcgen"
)

// InvoiceService = bukti transaksi bernomor (gapless per tahun) + credit note + PDF (docs/20 M31).
// Label "BUKTI TRANSAKSI": BUKAN faktur pajak (Q13 menunggu konsultan pajak).
type InvoiceService struct{ s *Services }

func newInvoiceService(s *Services) *InvoiceService { return &InvoiceService{s: s} }

// Issue menerbitkan invoice dalam transaksi pembayaran. Nomor dari penghitung per-tahun di tx yang sama → tanpa celah (T27).
func (i *InvoiceService) Issue(ctx context.Context, q *sqlcgen.Queries, o sqlcgen.Order, at time.Time) (sqlcgen.Invoice, error) {
	if inv, err := q.GetInvoiceByOrder(ctx, o.ID); err == nil {
		return inv, nil
	}
	year := at.Year()
	seq, err := q.NextCounter(ctx, sqlcgen.NextCounterParams{Kind: "invoice", Year: year})
	if err != nil {
		return sqlcgen.Invoice{}, err
	}
	return q.InsertInvoice(ctx, sqlcgen.InsertInvoiceParams{ID: id.New(), OrderID: o.ID, Number: fmt.Sprintf("INV/%d/%06d", year, seq), Year: year, Seq: seq,
		Subtotal: o.Subtotal + o.AddonsTotal, Discount: o.Discount, Fee: o.Fee, PaymentFee: o.PaymentFee, Tax: o.Tax, Total: o.Total})
}

// IssueCreditNote menerbitkan credit note untuk refund.
func (i *InvoiceService) IssueCreditNote(ctx context.Context, q *sqlcgen.Queries, orderID, refundID string, amount int64, at time.Time) (*sqlcgen.CreditNote, error) {
	inv, err := q.GetInvoiceByOrder(ctx, orderID)
	if err != nil {
		return nil, nil // order gratis/tanpa invoice
	}
	year := at.Year()
	seq, err := q.NextCounter(ctx, sqlcgen.NextCounterParams{Kind: "credit_note", Year: year})
	if err != nil {
		return nil, err
	}
	cn, err := q.InsertCreditNote(ctx, sqlcgen.InsertCreditNoteParams{ID: id.New(), InvoiceID: inv.ID, RefundID: &refundID, Number: fmt.Sprintf("CN/%d/%06d", year, seq), Amount: amount})
	if err != nil {
		return nil, err
	}
	return &cn, nil
}

// PDF membuat dokumen PDF bukti transaksi untuk pemilik order.
func (i *InvoiceService) PDF(ctx context.Context, userID, orderID string) ([]byte, string, error) {
	ord, err := i.s.Order.own(ctx, i.s.Q, userID, orderID)
	if err != nil {
		return nil, "", err
	}
	return i.render(ctx, ord)
}

func (i *InvoiceService) render(ctx context.Context, ord sqlcgen.Order) ([]byte, string, error) {
	inv, err := i.s.Q.GetInvoiceByOrder(ctx, ord.ID)
	if err != nil {
		return nil, "", apperr.NotFound("invoice (pesanan belum dibayar)")
	}
	ev, err := i.s.Q.GetEvent(ctx, ord.EventID)
	if err != nil {
		return nil, "", err
	}
	ven, _ := i.s.Q.GetVenue(ctx, ev.VenueID)
	buyer, _ := i.s.Q.GetUserByID(ctx, ord.UserID)
	items, _ := i.s.Q.ListOrderItems(ctx, ord.ID)
	addons, _ := i.s.Q.ListOrderAddons(ctx, ord.ID)
	cns, _ := i.s.Q.ListCreditNotes(ctx, inv.ID)
	pol := ParseRefundPolicy(ord.RefundPolicySnapshot, i.s.Cfg.RefundRescheduleWindowDays)

	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetMargins(15, 15, 15)
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(0, 10, tr("BUKTI TRANSAKSI"), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(0, 5, tr(i.s.Cfg.InvoiceSellerName), "", 1, "L", false, 0, "")
	if a := i.s.Cfg.InvoiceSellerAddress; a != "" {
		pdf.MultiCell(0, 5, tr(a), "", "L", false)
	}
	pdf.Ln(3)
	row := func(k, v string) {
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(40, 6, tr(k), "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 6, tr(v), "", "L", false)
	}
	paid := ""
	if ord.PaidAt != nil {
		paid = notify.FormatTime(*ord.PaidAt, ven.Tz)
	}
	row("Nomor", inv.Number)
	row("Tanggal terbit", notify.FormatTime(inv.IssuedAt, ven.Tz))
	row("Pesanan", ord.ID)
	row("Pembeli", buyer.Name+" <"+buyer.Email+">")
	row("Acara", ev.Title+" - "+ven.Name+", "+ven.City)
	row("Waktu acara", notify.FormatTime(ev.StartsAt, ven.Tz))
	row("Dibayar", paid+" ("+deref(ord.PaymentMethod)+")")
	pdf.Ln(3)
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(110, 7, tr("Rincian"), "B", 0, "L", false, 0, "")
	pdf.CellFormat(25, 7, tr("Qty"), "B", 0, "R", false, 0, "")
	pdf.CellFormat(45, 7, tr("Jumlah"), "B", 1, "R", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	line := func(name string, qty int, amt int64) {
		pdf.CellFormat(110, 6, tr(name), "", 0, "L", false, 0, "")
		pdf.CellFormat(25, 6, fmt.Sprint(qty), "", 0, "R", false, 0, "")
		pdf.CellFormat(45, 6, tr(notify.FormatIDR(amt)), "", 1, "R", false, 0, "")
	}
	byTier := map[string]*struct {
		name  string
		qty   int
		price int64
	}{}
	var order []string
	for _, it := range items {
		x, ok := byTier[it.TierID]
		if !ok {
			x = &struct {
				name  string
				qty   int
				price int64
			}{name: "Tiket " + it.TierName, price: it.Price}
			byTier[it.TierID] = x
			order = append(order, it.TierID)
		}
		x.qty++
	}
	for _, k := range order {
		x := byTier[k]
		line(x.name, x.qty, x.price*int64(x.qty))
	}
	for _, a := range addons {
		line(a.Name, a.Qty, a.Price*int64(a.Qty))
	}
	if ord.Discount > 0 {
		line("Diskon", 1, -ord.Discount)
	}
	if ord.Fee > 0 {
		line("Biaya layanan", 1, ord.Fee)
	}
	if ord.PaymentFee > 0 {
		line("Biaya pembayaran", 1, ord.PaymentFee)
	}
	if ord.Tax > 0 {
		line("Pajak", 1, ord.Tax)
	}
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(135, 8, tr("TOTAL"), "T", 0, "L", false, 0, "")
	pdf.CellFormat(45, 8, tr(notify.FormatIDR(ord.Total)), "T", 1, "R", false, 0, "")
	for _, cn := range cns {
		pdf.SetFont("Helvetica", "", 10)
		pdf.CellFormat(135, 6, tr("Credit note "+cn.Number), "", 0, "L", false, 0, "")
		pdf.CellFormat(45, 6, tr("-"+notify.FormatIDR(cn.Amount)), "", 1, "R", false, 0, "")
	}
	pdf.Ln(4)
	pdf.SetFont("Helvetica", "I", 9)
	pdf.MultiCell(0, 5, tr("Kebijakan refund saat pembelian: "+pol.Summary("id")), "", "L", false)
	pdf.Ln(2)
	pdf.MultiCell(0, 5, tr("Dokumen ini adalah bukti transaksi dan bukan faktur pajak."), "", "L", false)
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "invoice-" + fmt.Sprintf("%d-%06d", inv.Year, inv.Seq) + ".pdf", nil
}
