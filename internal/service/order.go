package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/payment"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// OrderService = quote, pembuatan order (idempoten lewat middleware), pembayaran, pembatalan, kedaluwarsa (docs/20 M7, 11 §2).
type OrderService struct{ s *Services }

func newOrderService(s *Services) *OrderService { return &OrderService{s: s} }

type OrderAttendee struct {
	SeatID string `json:"seat_id" binding:"omitempty,len=26"`
	Name   string `json:"name" binding:"required,min=2,max=100"`
	Email  string `json:"email" binding:"required,max=254"`
}

type OrderAddonSel struct {
	AddonID string `json:"addon_id" binding:"required,len=26"`
	Qty     int    `json:"qty" binding:"required,min=1,max=10"`
}

type CreateOrderReq struct {
	HoldID       string          `json:"hold_id" binding:"required,len=26"`
	Attendees    []OrderAttendee `json:"attendees" binding:"omitempty,max=50,dive"`
	PromoCode    string          `json:"promo_code" binding:"omitempty,max=32"`
	Addons       []OrderAddonSel `json:"addons" binding:"omitempty,max=10,dive"`
	AgeConfirmed bool            `json:"age_confirmed"`
	CampaignSlug string          `json:"campaign_slug" binding:"omitempty,max=64"`
}

type QuoteReq struct {
	HoldID    string          `json:"hold_id" binding:"required,len=26"`
	PromoCode string          `json:"promo_code" binding:"omitempty,max=32"`
	Addons    []OrderAddonSel `json:"addons" binding:"omitempty,max=10,dive"`
}

// unit = satu tiket yang akan dibuat.
type unit struct {
	SeatID    *string
	SeatLabel *string
	TierID    string
	TierName  string
	Price     int64
}

// buildUnits memperluas item hold menjadi unit tiket + baris harga.
func buildUnits(items []sqlcgen.ListHoldItemsRow) ([]unit, []PriceLine) {
	var units []unit
	agg := map[string]*PriceLine{}
	var order []string
	for _, it := range items {
		for i := 0; i < it.Qty; i++ {
			units = append(units, unit{SeatID: it.SeatID, SeatLabel: it.Label, TierID: it.TierID, TierName: it.TierName, Price: it.Price})
		}
		pl, ok := agg[it.TierID]
		if !ok {
			pl = &PriceLine{TierID: it.TierID, Name: it.TierName, Unit: it.Price}
			agg[it.TierID] = pl
			order = append(order, it.TierID)
		}
		pl.Qty += it.Qty
	}
	lines := make([]PriceLine, 0, len(order))
	for _, k := range order {
		lines = append(lines, *agg[k])
	}
	return units, lines
}

func (o *OrderService) loadAddons(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, sel []OrderAddonSel) ([]AddonLine, error) {
	var out []AddonLine
	seen := map[string]bool{}
	for _, a := range sel {
		if seen[a.AddonID] {
			return nil, apperr.Validation("add-on duplikat").With("addon_id", a.AddonID)
		}
		seen[a.AddonID] = true
		ad, err := q.GetAddon(ctx, a.AddonID)
		if err != nil || ad.EventID != ev.ID || !ad.Active {
			return nil, apperr.Validation("add-on tidak tersedia").With("addon_id", a.AddonID)
		}
		if ad.Sold+a.Qty > ad.Quota {
			return nil, apperr.Conflict(apperr.CodeSoldOut, "kuota add-on tidak mencukupi").With("addon_id", a.AddonID)
		}
		out = append(out, AddonLine{AddonID: ad.ID, Name: ad.Name, Unit: ad.Price, Qty: a.Qty})
	}
	return out, nil
}

// Quote = pratinjau harga akhir (F1) tanpa mengonsumsi apa pun.
func (o *OrderService) Quote(ctx context.Context, userID string, r QuoteReq) (*Quote, error) {
	hold, err := o.s.Hold.own(ctx, o.s.Q, userID, r.HoldID, false)
	if err != nil {
		return nil, err
	}
	ev, err := o.s.Q.GetEvent(ctx, hold.EventID)
	if err != nil {
		return nil, err
	}
	items, err := o.s.Q.ListHoldItems(ctx, hold.ID)
	if err != nil {
		return nil, err
	}
	_, lines := buildUnits(items)
	addons, err := o.loadAddons(ctx, o.s.Q, ev, r.Addons)
	if err != nil {
		return nil, err
	}
	var discount int64
	var code *string
	if r.PromoCode != "" {
		promo, d, err := o.s.Promo.evaluate(ctx, o.s.Q, ev, userID, r.PromoCode, lines)
		if err != nil {
			return nil, err
		}
		discount, code = d, &promo.Code
	}
	q := ComputeQuote(feeConfigOf(ev), lines, addons, discount)
	q.PromoCode = code
	return &q, nil
}

type OrderItemView struct {
	ID            string  `json:"id"`
	SeatID        *string `json:"seat_id,omitempty"`
	SeatLabel     *string `json:"seat_label,omitempty"`
	TierID        string  `json:"tier_id"`
	TierName      string  `json:"tier_name"`
	Price         int64   `json:"price"`
	AttendeeName  string  `json:"attendee_name"`
	AttendeeEmail string  `json:"attendee_email"`
}

type PaymentView struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Method      *string `json:"method,omitempty"`
	SnapToken   *string `json:"snap_token,omitempty"`
	RedirectUrl *string `json:"redirect_url,omitempty"`
}

type OrderView struct {
	ID            string          `json:"id"`
	EventID       string          `json:"event_id"`
	EventTitle    string          `json:"event_title"`
	EventSlug     string          `json:"event_slug"`
	Status        string          `json:"status"`
	Quote         Quote           `json:"quote"`
	Items         []OrderItemView `json:"items"`
	ExpiresAt     time.Time       `json:"expires_at"`
	ExpiresIn     int             `json:"expires_in"`
	PaidAt        *time.Time      `json:"paid_at,omitempty"`
	PaymentMethod *string         `json:"payment_method,omitempty"`
	Payment       *PaymentView    `json:"payment,omitempty"`
	RefundPolicy  RefundPolicy    `json:"refund_policy"`
	RefundSummary string          `json:"refund_summary"`
	TicketIDs     []string        `json:"ticket_ids,omitempty"`
	InvoiceNumber *string         `json:"invoice_number,omitempty"`
	CanPay        bool            `json:"can_pay"`
	CanExtend     bool            `json:"can_extend"`
	CreatedAt     time.Time       `json:"created_at"`
}

func (o *OrderService) view(ctx context.Context, q *sqlcgen.Queries, ord sqlcgen.Order) (*OrderView, error) {
	ev, err := q.GetEvent(ctx, ord.EventID)
	if err != nil {
		return nil, err
	}
	items, err := q.ListOrderItems(ctx, ord.ID)
	if err != nil {
		return nil, err
	}
	addons, err := q.ListOrderAddons(ctx, ord.ID)
	if err != nil {
		return nil, err
	}
	pol := ParseRefundPolicy(ord.RefundPolicySnapshot, o.s.Cfg.RefundRescheduleWindowDays)
	v := &OrderView{ID: ord.ID, EventID: ev.ID, EventTitle: ev.Title, EventSlug: ev.Slug, Status: ord.Status, ExpiresAt: ord.ExpiresAt, PaidAt: ord.PaidAt,
		PaymentMethod: ord.PaymentMethod, RefundPolicy: pol, RefundSummary: pol.Summary("id"), CreatedAt: ord.CreatedAt}
	if d := ord.ExpiresAt.Sub(o.s.now()); d > 0 && ord.Status == "pending" {
		v.ExpiresIn = int(d.Seconds())
	}
	var lines []PriceLine
	idx := map[string]int{}
	for _, it := range items {
		v.Items = append(v.Items, OrderItemView{ID: it.ID, SeatID: it.SeatID, SeatLabel: it.SeatLabel, TierID: it.TierID, TierName: it.TierName, Price: it.Price,
			AttendeeName: it.AttendeeName, AttendeeEmail: it.AttendeeEmail})
		i, ok := idx[it.TierID]
		if !ok {
			i = len(lines)
			idx[it.TierID] = i
			lines = append(lines, PriceLine{TierID: it.TierID, Name: it.TierName, Unit: it.Price})
		}
		lines[i].Qty++
	}
	var al []AddonLine
	for _, a := range addons {
		al = append(al, AddonLine{AddonID: a.AddonID, Name: a.Name, Unit: a.Price, Qty: a.Qty})
	}
	// kutipan direkonstruksi dari kolom yang tersimpan (angka tersimpan = sumber kebenaran)
	qt := ComputeQuote(FeeConfig{BP: 0, Payer: "buyer"}, lines, al, ord.Discount)
	qt.Fee, qt.PlatformFee, qt.Tax, qt.Total, qt.FeePayer = ord.Fee, ord.PlatformFee, ord.Tax, ord.Total, ev.FeePayer
	for i := range qt.Lines {
		if n := int64(qt.Lines[i].Qty); n > 0 && ord.Fee > 0 && ord.Subtotal > 0 {
			qt.Lines[i].UnitFee = feeConfigOf(ev).BuyerFee(qt.Lines[i].Unit)
		}
	}
	if ord.PromoID != nil {
		if p, err := q.GetPromo(ctx, *ord.PromoID); err == nil {
			qt.PromoCode = &p.Code
		}
	}
	v.Quote = qt
	if pay, err := q.LatestPaymentForOrder(ctx, ord.ID); err == nil {
		v.Payment = &PaymentView{ID: pay.ID, Status: pay.Status, Method: pay.Method, SnapToken: pay.SnapToken, RedirectUrl: pay.RedirectUrl}
	}
	if ord.Status == "paid" || ord.Status == "refunding" || ord.Status == "refunded" || ord.Status == "partially_refunded" {
		if tks, err := q.ListOrderTickets(ctx, ord.ID); err == nil {
			for _, t := range tks {
				v.TicketIDs = append(v.TicketIDs, t.ID)
			}
		}
		if inv, err := q.GetInvoiceByOrder(ctx, ord.ID); err == nil {
			v.InvoiceNumber = &inv.Number
		}
	}
	v.CanPay = ord.Status == "pending" && ord.Total > 0 && ord.ExpiresAt.After(o.s.now())
	if hold := ord.HoldID; hold != nil && ord.Status == "pending" {
		if h, err := q.GetHold(ctx, *hold); err == nil && h.ExtendedAt == nil && ord.ExpiresAt.After(o.s.now()) {
			if _, err := q.LatestPaymentForOrder(ctx, ord.ID); platform.IsNoRows(err) {
				v.CanExtend = true
			}
		}
	}
	return v, nil
}

// Create mengubah hold aktif menjadi order pending (atau lunas langsung bila gratis).
func (o *OrderService) Create(ctx context.Context, userID string, r CreateOrderReq) (*OrderView, error) {
	var out sqlcgen.Order
	var ev sqlcgen.Event
	var settled bool
	var user sqlcgen.User
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.LockUser(ctx, userID); err != nil {
			return err
		}
		hold, err := o.s.Hold.own(ctx, q, userID, r.HoldID, true)
		if err != nil {
			return err
		}
		now := o.s.now()
		if hold.Status != "active" || !hold.ExpiresAt.After(now) {
			return apperr.Conflict(apperr.CodeHoldExpired, "hold sudah kedaluwarsa atau tidak aktif; pilih ulang")
		}
		ev, err = q.GetEvent(ctx, hold.EventID)
		if err != nil {
			return err
		}
		if ev.Status != stPublished || ev.SalesPaused {
			return apperr.Conflict(apperr.CodeSalePaused, "penjualan sedang dijeda")
		}
		user, err = o.s.Auth.RequireVerified(ctx, q, userID)
		if err != nil {
			return err
		}
		if err := o.s.Legal.RequireConsents(ctx, q, userID); err != nil {
			return err
		}
		if ev.AgeMin != nil && *ev.AgeMin > 0 && !r.AgeConfirmed {
			return apperr.Unprocessable(apperr.CodeAgeConfirmation, fmt.Sprintf("konfirmasi usia minimal %d tahun diperlukan", *ev.AgeMin)).With("age_min", *ev.AgeMin)
		}
		items, err := q.ListHoldItems(ctx, hold.ID)
		if err != nil {
			return err
		}
		units, lines := buildUnits(items)
		if len(units) == 0 {
			return apperr.Conflict(apperr.CodeInvalidState, "hold kosong")
		}
		atts, err := o.attendeesFor(units, r.Attendees, user)
		if err != nil {
			return err
		}
		addons, err := o.loadAddons(ctx, q, ev, r.Addons)
		if err != nil {
			return err
		}
		var promo *sqlcgen.PromoCode
		var discount int64
		if r.PromoCode != "" {
			promo, discount, err = o.s.Promo.evaluate(ctx, q, ev, userID, r.PromoCode, lines)
			if err != nil {
				return err
			}
		}
		quote := ComputeQuote(feeConfigOf(ev), lines, addons, discount)
		pol := ParseRefundPolicy(ev.RefundPolicy, o.s.Cfg.RefundRescheduleWindowDays)
		orderID := id.New()
		out, err = q.InsertOrder(ctx, sqlcgen.InsertOrderParams{
			ID: orderID, UserID: userID, EventID: ev.ID, HoldID: &hold.ID, Subtotal: quote.Subtotal, AddonsTotal: quote.AddonsTotal,
			Discount: quote.Discount, Fee: quote.Fee, PlatformFee: quote.PlatformFee, PaymentFee: 0, Tax: 0, Total: quote.Total,
			PromoID: func() *string {
				if promo != nil {
					return &promo.ID
				}
				return nil
			}(),
			CampaignSlug: strPtr(r.CampaignSlug), AgeConfirmed: r.AgeConfirmed, RefundPolicySnapshot: pol.JSON(), ExpiresAt: hold.ExpiresAt,
		})
		if err != nil {
			return err
		}
		for i, u := range units {
			if err := q.InsertOrderItem(ctx, sqlcgen.InsertOrderItemParams{ID: id.New(), OrderID: orderID, SeatID: u.SeatID, TierID: u.TierID, Price: u.Price,
				AttendeeName: atts[i].Name, AttendeeEmail: atts[i].Email}); err != nil {
				return err
			}
		}
		for _, a := range addons {
			n, err := q.ReserveAddon(ctx, sqlcgen.ReserveAddonParams{ID: a.AddonID, Qty: a.Qty})
			if err != nil {
				return err
			}
			if n == 0 {
				return apperr.Conflict(apperr.CodeSoldOut, "kuota add-on tidak mencukupi").With("addon_id", a.AddonID)
			}
			if err := q.InsertOrderAddon(ctx, sqlcgen.InsertOrderAddonParams{OrderID: orderID, AddonID: a.AddonID, Qty: a.Qty, Price: a.Unit}); err != nil {
				return err
			}
		}
		if promo != nil {
			if err := o.s.Promo.consume(ctx, q, *promo, orderID, userID, quote.Discount); err != nil {
				return err
			}
		}
		if err := q.SetHoldStatus(ctx, sqlcgen.SetHoldStatusParams{ID: hold.ID, Status: "converted", OrderID: &orderID}); err != nil {
			return err
		}
		o.s.Waitlist.OnHoldEnded(ctx, q, hold.ID, "accepted")
		if quote.Total == 0 { // gratis: lunas langsung, tanpa gateway
			settled = true
			if err := o.s.Payment.settleTx(ctx, q, ev, &out, now, "free", false); err != nil {
				return err
			}
		}
		return emit(ctx, q, "user."+userID, "order.created", map[string]any{"order_id": orderID})
	})
	if err != nil {
		return nil, err
	}
	o.s.Queue.FreeSlot(ctx, ev.ID, userID, true)
	if settled {
		o.s.Payment.afterSettle(ctx, ev, out, user)
	}
	return o.view(ctx, o.s.Q, out)
}

func (o *OrderService) attendeesFor(units []unit, given []OrderAttendee, buyer sqlcgen.User) ([]OrderAttendee, error) {
	out := make([]OrderAttendee, len(units))
	if len(given) == 0 { // F2: data peserta dapat menyusul; default = pembeli
		for i := range out {
			out[i] = OrderAttendee{Name: buyer.Name, Email: buyer.Email}
		}
		return out, nil
	}
	norm := func(a OrderAttendee) (OrderAttendee, error) {
		e, ok := NormalizeEmail(a.Email)
		if !ok {
			return a, apperr.Validation("email peserta tidak valid").With("field", "attendees.email")
		}
		a.Email, a.Name = e, strings.TrimSpace(a.Name)
		return a, nil
	}
	if units[0].SeatID != nil {
		bySeat := map[string]OrderAttendee{}
		for _, a := range given {
			if a.SeatID == "" {
				return nil, apperr.Validation("attendees pada event seated harus memuat seat_id").With("field", "attendees.seat_id")
			}
			n, err := norm(a)
			if err != nil {
				return nil, err
			}
			bySeat[a.SeatID] = n
		}
		for i, u := range units {
			a, ok := bySeat[*u.SeatID]
			if !ok {
				return nil, apperr.Validation("data peserta kursi ini belum diisi").With("seat_id", *u.SeatID)
			}
			out[i] = a
		}
		return out, nil
	}
	if len(given) != len(units) {
		return nil, apperr.Validation("jumlah attendees harus sama dengan jumlah tiket").With("expected", len(units))
	}
	for i, a := range given {
		n, err := norm(a)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

func (o *OrderService) own(ctx context.Context, q *sqlcgen.Queries, userID, orderID string) (sqlcgen.Order, error) {
	ord, err := q.GetOrder(ctx, orderID)
	if err != nil || ord.UserID != userID {
		return ord, apperr.NotFound("pesanan")
	}
	return ord, nil
}

func (o *OrderService) Get(ctx context.Context, userID, orderID string) (*OrderView, error) {
	ord, err := o.own(ctx, o.s.Q, userID, orderID)
	if err != nil {
		return nil, err
	}
	return o.view(ctx, o.s.Q, ord)
}

type OrderBrief struct {
	ID         string    `json:"id"`
	EventTitle string    `json:"event_title"`
	EventSlug  string    `json:"event_slug"`
	Status     string    `json:"status"`
	Total      int64     `json:"total"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	PosterURL  string    `json:"poster_url,omitempty"`
	StartsAt   time.Time `json:"starts_at"`
}

func (o *OrderService) List(ctx context.Context, userID string, limit, offset int) ([]OrderBrief, int, error) {
	rows, err := o.s.Q.ListUserOrders(ctx, sqlcgen.ListUserOrdersParams{UserID: userID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := o.s.Q.CountUserOrders(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	out := make([]OrderBrief, 0, len(rows))
	for _, r := range rows {
		b := OrderBrief{ID: r.ID, EventTitle: r.EventTitle, EventSlug: r.EventSlug, Status: r.Status, Total: r.Total, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, StartsAt: r.EventStartsAt}
		if r.EventPosterKey != nil {
			b.PosterURL = MediaURL(*r.EventPosterKey, 800)
		}
		out = append(out, b)
	}
	return out, int(n), nil
}

// Cancel membatalkan order pending milik pengguna sebelum dibayar (kursi dilepas).
func (o *OrderService) Cancel(ctx context.Context, userID, orderID string) error {
	var ord sqlcgen.Order
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		ord, err = q.GetOrderForUpdate(ctx, orderID)
		if err != nil || ord.UserID != userID {
			return apperr.NotFound("pesanan")
		}
		if ord.Status != "pending" {
			return apperr.Conflict(apperr.CodeOrderNotPayable, "hanya pesanan pending yang dapat dibatalkan")
		}
		if pay, err := q.LatestPaymentForOrder(ctx, ord.ID); err == nil && pay.Status == "settled" {
			return apperr.Conflict(apperr.CodeOrderNotPayable, "pembayaran sudah diterima")
		}
		return o.releaseOrderTx(ctx, q, ord, "cancelled", "released")
	})
	if err != nil {
		return err
	}
	o.afterRelease(ctx, ord)
	return nil
}

// releaseOrderTx mengembalikan stok/kuota/promo dan menutup order (expired|cancelled).
func (o *OrderService) releaseOrderTx(ctx context.Context, q *sqlcgen.Queries, ord sqlcgen.Order, orderStatus, holdStatus string) error {
	if ord.HoldID != nil {
		hold, err := q.GetHoldForUpdate(ctx, *ord.HoldID)
		if err == nil && (hold.Status == "converted" || hold.Status == "active") {
			if err := o.s.Hold.releaseTx(ctx, q, hold, holdStatus); err != nil {
				return err
			}
		}
	}
	if ord.ResaleListingID != nil { // order resale: reservasi listing dilepas
		if _, err := q.ReleaseListingByOrder(ctx, &ord.ID); err != nil {
			return err
		}
	}
	addons, err := q.ListOrderAddons(ctx, ord.ID)
	if err != nil {
		return err
	}
	for _, a := range addons {
		if err := q.ReleaseAddon(ctx, sqlcgen.ReleaseAddonParams{ID: a.AddonID, Qty: a.Qty}); err != nil {
			return err
		}
	}
	if err := o.s.Promo.release(ctx, q, ord.ID); err != nil {
		return err
	}
	if err := q.SetOrderStatus(ctx, sqlcgen.SetOrderStatusParams{ID: ord.ID, Status: orderStatus}); err != nil {
		return err
	}
	return emit(ctx, q, "user."+ord.UserID, "order."+orderStatus, map[string]any{"order_id": ord.ID})
}

func (o *OrderService) afterRelease(ctx context.Context, ord sqlcgen.Order) {
	o.s.Queue.FreeSlot(ctx, ord.EventID, ord.UserID, false)
	o.s.Catalog.InvalidateAvailability(ctx, ord.EventID)
	if pay, err := o.s.Q.LatestPaymentForOrder(ctx, ord.ID); err == nil && (pay.Status == "created" || pay.Status == "pending") {
		if err := o.s.Gateway.Expire(ctx, deref(pay.ProviderRef)); err != nil {
			o.s.Log.Error("gateway expire gagal", "order", ord.ID, "error", err)
		}
	}
	o.s.Waitlist.OnInventoryFreed(ctx, ord.EventID)
}

// ExpireDue: order pending lewat batas → expired, stok dikembalikan (worker, ±1 menit; docs/04 §9).
func (o *OrderService) ExpireDue(ctx context.Context) (int, error) {
	total := 0
	for {
		var done []sqlcgen.Order
		err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			due, err := q.DueExpiredOrders(ctx, sqlcgen.DueExpiredOrdersParams{ExpiresAt: o.s.now(), Limit: 100})
			if err != nil {
				return err
			}
			for _, ord := range due {
				if err := o.releaseOrderTx(ctx, q, ord, "expired", "expired"); err != nil {
					return err
				}
				done = append(done, ord)
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		for _, ord := range done {
			o.afterRelease(ctx, ord)
			ev, _ := o.s.Q.GetEvent(ctx, ord.EventID)
			o.s.Notifier.Send(ctx, NotifyReq{UserID: ord.UserID, Template: "order_expired", Data: map[string]any{"name": "", "event_title": ev.Title}})
		}
		total += len(done)
		if len(done) < 100 {
			return total, nil
		}
	}
}

// ---------- Pembayaran ----------

type PayResult struct {
	Gateway     string `json:"gateway"` // midtrans | xendit | …: menentukan cara klien membuka pembayaran (Snap vs redirect_url)
	PaymentID   string `json:"payment_id"`
	SnapToken   string `json:"snap_token"`
	RedirectUrl string `json:"redirect_url"`
	ClientKey   string `json:"client_key,omitempty"`
	Env         string `json:"env"`
	Reused      bool   `json:"reused"`
}

// Pay membuat/menggunakan ulang transaksi Snap untuk order pending (idempoten per percobaan aktif).
// payClaimStale = umur maksimum claim pembayaran tanpa token sebelum dianggap yatim.
const payClaimStale = 2 * time.Minute

func (o *OrderService) Pay(ctx context.Context, userID, orderID string) (*PayResult, error) {
	ord, err := o.own(ctx, o.s.Q, userID, orderID)
	if err != nil {
		return nil, err
	}
	now := o.s.now()
	if ord.Status != "pending" || ord.Total <= 0 || !ord.ExpiresAt.After(now) {
		return nil, apperr.Unprocessable(apperr.CodeOrderNotPayable, "pesanan tidak dapat dibayar (status/kedaluwarsa)").With("status", ord.Status)
	}
	if ok, _, err := o.s.Limiter.Allow(ctx, "pay:"+userID, 20, time.Minute); err == nil && !ok {
		return nil, apperr.TooMany("terlalu sering meminta pembayaran")
	}
	ev, err := o.s.Q.GetEvent(ctx, ord.EventID)
	if err != nil {
		return nil, err
	}
	if ev.SalesPaused {
		return nil, apperr.Conflict(apperr.CodeSalePaused, "penjualan sedang dijeda")
	}
	user, err := o.s.Q.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	items, err := o.s.Q.ListOrderItems(ctx, ord.ID)
	if err != nil {
		return nil, err
	}
	// Claim percobaan bayar secara serial per order (advisory lock hanya selama transaksi singkat ini,
	// BUKAN selama panggilan gateway): baris payment dibuat SEBELUM gateway dipanggil, sehingga klik ganda /
	// permintaan paralel tidak membuat dua transaksi gateway maupun dua baris aktif.
	var (
		pay    sqlcgen.Payment
		reused *PayResult
		ref    string
	)
	err = o.s.InTxRaw(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "pay:"+ord.ID); err != nil {
			return err
		}
		last, lerr := q.LatestPaymentForOrder(ctx, ord.ID)
		if lerr != nil && !platform.IsNoRows(lerr) {
			return lerr
		}
		if lerr == nil && (last.Status == "created" || last.Status == "pending") {
			if last.SnapToken != nil { // pakai ulang percobaan aktif
				reused = &PayResult{Gateway: o.s.Gateway.Name(), PaymentID: last.ID, SnapToken: *last.SnapToken, RedirectUrl: deref(last.RedirectUrl), ClientKey: o.s.Cfg.MidtransClientKey, Env: o.s.Cfg.MidtransEnv, Reused: true}
				return nil
			}
			if time.Since(last.CreatedAt) < payClaimStale { // pemanggil lain sedang memanggil gateway
				return apperr.Conflict(apperr.CodeConflict, "pembayaran sedang disiapkan; coba lagi sebentar")
			}
			// claim yatim (proses mati sebelum gateway menjawab): tandai gagal, buat percobaan baru.
			if err := q.UpdatePaymentStatus(ctx, sqlcgen.UpdatePaymentStatusParams{ID: last.ID, Status: "failed"}); err != nil {
				return err
			}
		}
		var attempt int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM payments WHERE order_id=$1", ord.ID).Scan(&attempt); err != nil {
			return err
		}
		ref = ord.ID
		if attempt > 0 {
			ref = fmt.Sprintf("%s-%d", ord.ID, attempt+1) // order_id gateway harus unik per percobaan
		}
		var ierr error
		pay, ierr = q.InsertPayment(ctx, sqlcgen.InsertPaymentParams{ID: id.New(), OrderID: ord.ID, Provider: o.s.Gateway.Name(), ProviderRef: &ref, Amount: ord.Total, Status: "pending"})
		return ierr
	})
	if err != nil {
		return nil, err
	}
	if reused != nil {
		return reused, nil
	}
	res, err := o.s.Gateway.CreateTransaction(ctx, payment.CreateTxReq{
		OrderRef: ref, Amount: ord.Total, Customer: payment.Customer{Name: user.Name, Email: user.Email, Phone: deref(user.Phone)},
		Items: o.gatewayItems(ctx, ord, items), ExpiresAt: ord.ExpiresAt, Now: now, CallbackURL: o.s.Cfg.PublicWebURL + "/orders/" + ord.ID,
	})
	if err != nil {
		o.s.Log.Error("create transaction gagal", "order", ord.ID, "error", err)
		// percobaan yang di-claim ditandai gagal agar percobaan berikutnya memakai ref baru.
		if uerr := o.s.Q.UpdatePaymentStatus(ctx, sqlcgen.UpdatePaymentStatusParams{ID: pay.ID, Status: "failed"}); uerr != nil {
			o.s.Log.Error("tandai payment gagal gagal", "payment", pay.ID, "error", uerr)
		}
		return nil, apperr.New(502, apperr.CodePaymentFailed, "gateway pembayaran sedang bermasalah; coba lagi sebentar")
	}
	if err := o.s.Q.SetPaymentIntent(ctx, sqlcgen.SetPaymentIntentParams{ID: pay.ID, SnapToken: &res.Token, RedirectUrl: &res.RedirectURL}); err != nil {
		return nil, err
	}
	return &PayResult{Gateway: o.s.Gateway.Name(), PaymentID: pay.ID, SnapToken: res.Token, RedirectUrl: res.RedirectURL, ClientKey: o.s.Cfg.MidtransClientKey, Env: o.s.Cfg.MidtransEnv}, nil
}

// gatewayItems menyusun item_details yang jumlahnya = gross_amount (syarat Midtrans).
func (o *OrderService) gatewayItems(ctx context.Context, ord sqlcgen.Order, items []sqlcgen.ListOrderItemsRow) []payment.Item {
	agg := map[string]*payment.Item{}
	var order []string
	for _, it := range items {
		x, ok := agg[it.TierID]
		if !ok {
			x = &payment.Item{ID: it.TierID, Name: "Tiket " + it.TierName, Price: it.Price}
			agg[it.TierID] = x
			order = append(order, it.TierID)
		}
		x.Quantity++
	}
	var out []payment.Item
	var sum int64
	for _, k := range order {
		out = append(out, *agg[k])
		sum += agg[k].Price * int64(agg[k].Quantity)
	}
	if ads, err := o.s.Q.ListOrderAddons(ctx, ord.ID); err == nil {
		for _, a := range ads {
			out = append(out, payment.Item{ID: a.AddonID, Name: a.Name, Price: a.Price, Quantity: a.Qty})
			sum += a.Price * int64(a.Qty)
		}
	}
	if ord.Fee > 0 {
		out = append(out, payment.Item{ID: "fee", Name: "Biaya layanan", Price: ord.Fee, Quantity: 1})
		sum += ord.Fee
	}
	if ord.Discount > 0 {
		out = append(out, payment.Item{ID: "discount", Name: "Diskon", Price: -ord.Discount, Quantity: 1})
		sum -= ord.Discount
	}
	if sum != ord.Total { // jaga konsistensi: satu item ringkas
		return []payment.Item{{ID: ord.ID, Name: "Pesanan TixGo", Price: ord.Total, Quantity: 1}}
	}
	return out
}
