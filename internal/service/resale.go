package service

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// ResaleService = resale resmi berplafon (docs/10 §9, M9 AC2): hanya lewat TixGo, harga ≤ plafon event, tiket baru untuk pembeli, tiket lama 'transferred'.
// Uang pembeli ditahan di akun ledger `resale_payable`; penjual dibayar MANUAL oleh finance (konsisten dengan Q12) setelah mengisi rekening.
type ResaleService struct{ s *Services }

func newResaleService(s *Services) *ResaleService { return &ResaleService{s: s} }

const (
	AcctResalePayable = "resale_payable"
	resaleMinPrice    = 1000
	resaleReserveTTL  = 10 * time.Minute
)

// resaleFee = biaya layanan pembeli (basis poin dari config), dibulatkan ke atas.
func (r *ResaleService) resaleFee(price int64) int64 {
	bp := int64(r.s.Cfg.ResaleFeeBP)
	return (price*bp + 9999) / 10000
}

type ListingView struct {
	ID        string    `json:"id"`
	Price     int64     `json:"price"`
	FacePrice int64     `json:"face_price"`
	BuyerFee  int64     `json:"buyer_fee"`
	BuyerPays int64     `json:"buyer_pays"`
	Status    string    `json:"status,omitempty"`
	Seat      *string   `json:"seat_label,omitempty"`
	TierName  string    `json:"tier_name,omitempty"`
	TicketID  string    `json:"ticket_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (r *ResaleService) view(l sqlcgen.ResaleListing) ListingView {
	fee := r.resaleFee(l.Price)
	return ListingView{ID: l.ID, Price: l.Price, FacePrice: l.FacePrice, BuyerFee: fee, BuyerPays: l.Price + fee, Status: l.Status, TicketID: l.TicketID, CreatedAt: l.CreatedAt}
}

// Cap = harga maksimum yang boleh dipasang untuk tiket ini.
func (r *ResaleService) Cap(face int64, pct int) int64 { return face * int64(pct) / 100 }

func (r *ResaleService) CreateListing(ctx context.Context, userID, ticketID string, price int64) (*ListingView, error) {
	if price < resaleMinPrice {
		return nil, apperr.Validation(fmt.Sprintf("harga minimal %d", resaleMinPrice)).With("field", "price")
	}
	u, err := r.s.Q.GetUserByID(ctx, userID)
	if err != nil || u.EmailVerifiedAt == nil {
		return nil, apperr.ForbiddenCode(apperr.CodeEmailNotVerified, "verifikasi email dulu untuk menjual tiket")
	}
	var out sqlcgen.ResaleListing
	err = r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		tk, err := q.GetTicketForUpdate(ctx, ticketID)
		if err != nil || tk.OwnerUserID != userID {
			return apperr.NotFound("tiket")
		}
		if tk.Status != "valid" {
			return apperr.Conflict(apperr.CodeTicketInvalid, "hanya tiket aktif yang bisa dijual")
		}
		if tk.Origin == "comp" {
			return apperr.Unprocessable(apperr.CodeNotAllowed, "tiket undangan tidak bisa dijual ulang")
		}
		ev, err := q.GetEvent(ctx, tk.EventID)
		if err != nil {
			return err
		}
		if ev.ResaleCapPct <= 0 {
			return apperr.Unprocessable(apperr.CodeNotAllowed, "penyelenggara tidak mengizinkan resale untuk event ini")
		}
		if (ev.Status != stPublished && ev.Status != stPaused) || !r.s.now().Before(ev.StartsAt) || ev.ScheduleState == "cancelled" {
			return apperr.Conflict(apperr.CodeInvalidState, "tiket event ini tidak dapat dijual saat ini")
		}
		if _, err := q.ActiveClaimLinkForTicket(ctx, sqlcgen.ActiveClaimLinkForTicketParams{TicketID: tk.ID, ExpiresAt: r.s.now()}); err == nil {
			return apperr.Conflict(apperr.CodeConflict, "tiket sedang dikirim lewat tautan; cabut tautan dulu")
		}
		face, err := q.TicketFacePrice(ctx, tk.ID)
		if err != nil {
			return err
		}
		if limit := r.Cap(face, ev.ResaleCapPct); price > limit {
			return apperr.Unprocessable(apperr.CodeValidation, "harga melebihi plafon resale").With("max_price", limit).With("face_price", face).With("cap_pct", ev.ResaleCapPct)
		}
		out, err = q.InsertListing(ctx, sqlcgen.InsertListingParams{ID: id.New(), TicketID: tk.ID, EventID: ev.ID, SellerID: userID, FacePrice: face, Price: price})
		if err != nil {
			if platform.IsUniqueViolation(err) {
				return apperr.Conflict(apperr.CodeConflict, "tiket ini sudah dijual")
			}
			return err
		}
		return r.s.Audit.Log(ctx, q, &auth.Principal{UserID: userID, Role: "customer"}, "resale.list", "ticket", tk.ID, nil, map[string]any{"price": price, "face": face})
	})
	if err != nil {
		return nil, err
	}
	v := r.view(out)
	return &v, nil
}

func (r *ResaleService) Cancel(ctx context.Context, userID, listingID string) error {
	n, err := r.s.Q.CancelListing(ctx, sqlcgen.CancelListingParams{ID: listingID, SellerID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.Conflict(apperr.CodeInvalidState, "listing tidak aktif (sudah terjual, dipesan pembeli, atau bukan milikmu)")
	}
	return nil
}

func (r *ResaleService) Mine(ctx context.Context, userID string) ([]ListingView, error) {
	rows, err := r.s.Q.ListSellerListings(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ListingView, 0, len(rows))
	for _, l := range rows {
		out = append(out, r.view(l))
	}
	return out, nil
}

// ForEvent (publik): tanpa identitas penjual.
func (r *ResaleService) ForEvent(ctx context.Context, eventKey string) (map[string]any, error) {
	ev, err := r.s.Catalog.lookup(ctx, eventKey)
	if err != nil || ev.Visibility != "public" || ev.ResaleCapPct <= 0 {
		return nil, apperr.NotFound("event")
	}
	rows, err := r.s.Q.ListEventListings(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	items := make([]ListingView, 0, len(rows))
	for _, l := range rows {
		fee := r.resaleFee(l.Price)
		items = append(items, ListingView{ID: l.ID, Price: l.Price, FacePrice: l.FacePrice, BuyerFee: fee, BuyerPays: l.Price + fee, Seat: l.SeatLabel, TierName: l.TierName, CreatedAt: l.CreatedAt})
	}
	return map[string]any{"cap_pct": ev.ResaleCapPct, "listings": items}, nil
}

// Buy memesan listing (reservasi 10 menit) dan membuat order pending; pembayaran lewat alur Order.Pay yang sama.
func (r *ResaleService) Buy(ctx context.Context, userID, listingID string) (*OrderView, error) {
	buyer, err := r.s.Q.GetUserByID(ctx, userID)
	if err != nil || buyer.EmailVerifiedAt == nil {
		return nil, apperr.ForbiddenCode(apperr.CodeEmailNotVerified, "verifikasi email dulu untuk membeli")
	}
	if r.s.Fraud.Decision(ctx, userID) == "block" {
		return nil, apperr.ForbiddenCode(apperr.CodeBlocked, "akses ditolak")
	}
	var orderID string
	err = r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		l, err := q.GetListingForUpdate(ctx, listingID)
		if err != nil {
			return apperr.NotFound("listing")
		}
		if l.Status != "active" {
			return apperr.Conflict(apperr.CodeSeatTaken, "tiket ini sudah dipesan atau terjual")
		}
		if l.SellerID == userID {
			return apperr.Conflict(apperr.CodeConflict, "tidak bisa membeli tiketmu sendiri")
		}
		tk, err := q.GetTicketForUpdate(ctx, l.TicketID)
		if err != nil || tk.Status != "valid" || tk.OwnerUserID != l.SellerID {
			_, _ = q.CancelListing(ctx, sqlcgen.CancelListingParams{ID: l.ID, SellerID: l.SellerID})
			return apperr.Conflict(apperr.CodeTicketInvalid, "tiket ini sudah tidak tersedia")
		}
		ev, err := q.GetEvent(ctx, l.EventID)
		if err != nil {
			return err
		}
		if (ev.Status != stPublished) || !r.s.now().Before(ev.StartsAt) || ev.ScheduleState == "cancelled" || ev.SalesPaused {
			return apperr.Conflict(apperr.CodeSaleNotOpen, "penjualan tiket event ini sedang tidak tersedia")
		}
		fee := r.resaleFee(l.Price)
		pol := DefaultRefundPolicy(r.s.Cfg.RefundRescheduleWindowDays)
		until := r.s.now().Add(resaleReserveTTL)
		orderID = id.New()
		if _, err := q.InsertResaleOrder(ctx, sqlcgen.InsertResaleOrderParams{ID: orderID, UserID: userID, EventID: ev.ID, Subtotal: l.Price, Fee: fee, Total: l.Price + fee,
			RefundPolicySnapshot: pol.JSON(), ExpiresAt: until, ResaleListingID: &l.ID}); err != nil {
			if platform.IsUniqueViolation(err) {
				return apperr.Conflict(apperr.CodeSeatTaken, "tiket ini sudah dipesan")
			}
			return err
		}
		if err := q.InsertOrderItem(ctx, sqlcgen.InsertOrderItemParams{ID: id.New(), OrderID: orderID, SeatID: tk.SeatID, TierID: tk.TierID, Price: l.Price, AttendeeName: buyer.Name, AttendeeEmail: buyer.Email}); err != nil {
			return err
		}
		n, err := q.ReserveListing(ctx, sqlcgen.ReserveListingParams{Buyer: &userID, Until: &until, OrderID: &orderID, ID: l.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.Conflict(apperr.CodeSeatTaken, "tiket ini sudah dipesan")
		}
		return r.s.Audit.Log(ctx, q, &auth.Principal{UserID: userID, Role: "customer"}, "resale.reserve", "listing", l.ID, nil, map[string]any{"order": orderID})
	})
	if err != nil {
		return nil, err
	}
	return r.s.Order.Get(ctx, userID, orderID)
}

// ReleaseStale (scheduler): reservasi yang order-nya sudah tidak pending dikembalikan jadi aktif.
func (r *ResaleService) ReleaseStale(ctx context.Context) (int, error) {
	rows, err := r.s.Q.StaleReservations(ctx, ptr(r.s.now()))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range rows {
		if x.OrderID != nil {
			if c, err := r.s.Q.ReleaseListingByOrder(ctx, x.OrderID); err == nil {
				n += int(c)
			}
		}
	}
	return n, nil
}

// settleTx dipanggil PaymentService di dalam transaksi pelunasan order resale.
func (r *ResaleService) settleTx(ctx context.Context, q *sqlcgen.Queries, ev sqlcgen.Event, ord *sqlcgen.Order, paidAt time.Time, method string) error {
	l, err := q.GetListingForUpdate(ctx, deref(ord.ResaleListingID))
	if err != nil {
		return err
	}
	if l.Status != "reserved" || deref(l.OrderID) != ord.ID {
		return errNoStock // reservasi sudah lepas (pesanan kedaluwarsa); uang dikembalikan otomatis
	}
	old, err := q.GetTicketForUpdate(ctx, l.TicketID)
	if err != nil {
		return err
	}
	if old.Status != "valid" || old.OwnerUserID != l.SellerID {
		return errNoStock // penjual sudah memakai/mengirim tiketnya
	}
	if err := q.MarkOrderPaid(ctx, sqlcgen.MarkOrderPaidParams{ID: ord.ID, PaidAt: &paidAt, PaymentMethod: strPtr(method)}); err != nil {
		return err
	}
	ord.Status, ord.PaidAt, ord.PaymentMethod = "paid", &paidAt, strPtr(method)
	buyer, err := q.GetUserByID(ctx, ord.UserID)
	if err != nil {
		return err
	}
	items, err := q.ListOrderItems(ctx, ord.ID)
	if err != nil || len(items) != 1 {
		return fmt.Errorf("order resale %s harus memiliki 1 item", ord.ID)
	}
	enc, err := r.s.Box.Encrypt([]byte(auth.NewTOTPSecret()))
	if err != nil {
		return err
	}
	num, err := q.NextEntryNumber(ctx)
	if err != nil {
		return err
	}
	name := buyer.Name
	nt, err := q.InsertTicket(ctx, sqlcgen.InsertTicketParams{ID: id.New(), OrderItemID: &items[0].ID, EventID: ev.ID, TierID: old.TierID, OwnerUserID: ord.UserID, SeatID: old.SeatID,
		SeatLabel: old.SeatLabel, EntryNumber: strPtr(fmt.Sprintf("%06d", num)), AttendeeName: &name, SecretEnc: enc, Origin: "transfer"})
	if err != nil {
		return err
	}
	face := l.FacePrice
	if err := q.SetTicketFacePrice(ctx, sqlcgen.SetTicketFacePriceParams{ID: nt.ID, FacePrice: &face}); err != nil {
		return err
	}
	if n, err := q.SetTicketStatusForEvent(ctx, sqlcgen.SetTicketStatusForEventParams{ID: old.ID, EventID: ev.ID, Status: "transferred"}); err != nil || n == 0 {
		if err == nil {
			err = fmt.Errorf("tiket lama tidak dapat ditandai transferred")
		}
		return err
	}
	if err := q.InsertTicketTransfer(ctx, sqlcgen.InsertTicketTransferParams{ID: id.New(), TicketID: nt.ID, FromUserID: l.SellerID, ToUserID: ord.UserID, Via: "resale", RefID: &l.ID}); err != nil {
		return err
	}
	if err := q.SetListingSold(ctx, sqlcgen.SetListingSoldParams{ID: l.ID, SoldAt: &paidAt}); err != nil {
		return err
	}
	if _, err := q.InsertResalePayout(ctx, sqlcgen.InsertResalePayoutParams{ID: id.New(), ListingID: l.ID, SellerID: l.SellerID, Amount: l.Price}); err != nil {
		return err
	}
	if _, err := r.s.Invoice.Issue(ctx, q, *ord, paidAt); err != nil {
		return err
	}
	if ord.Total != l.Price+ord.Fee {
		return fmt.Errorf("total order resale tidak konsisten")
	}
	if _, err := r.s.Ledger.Post(ctx, q, "order_paid", ord.ID, "pembayaran resale", []LedgerEntry{
		{Kind: AcctPaymentClearing, Debit: ord.Total},
		{Kind: AcctPlatformRevenue, Credit: ord.Fee},
		{Kind: AcctResalePayable, Credit: l.Price},
	}); err != nil {
		return err
	}
	if err := emit(ctx, q, "user."+l.SellerID, "resale.sold", map[string]any{"listing_id": l.ID}); err != nil {
		return err
	}
	if err := emit(ctx, q, "user."+ord.UserID, "order.paid", map[string]any{"order_id": ord.ID, "ticket_ids": []string{nt.ID}}); err != nil {
		return err
	}
	return nil
}

func (r *ResaleService) afterSettle(ctx context.Context, ord sqlcgen.Order) {
	l, err := r.s.Q.GetListing(ctx, deref(ord.ResaleListingID))
	if err != nil {
		return
	}
	ev, _ := r.s.Q.GetEvent(ctx, l.EventID)
	r.s.Notifier.Send(ctx, NotifyReq{UserID: l.SellerID, Template: "resale_sold", Data: map[string]any{
		"name": "", "event_title": ev.Title, "price": formatIDR(l.Price), "link": r.s.Cfg.PublicWebURL + "/penjualan"}})
}

// reverseLedger membalik pembukuan order resale (refund penuh pembeli, mis. event batal). Utang ke penjual berkurang; bila sudah dibayar, saldo menjadi negatif = piutang yang ditagih finance.
func (r *ResaleService) reverseLedger(ctx context.Context, q *sqlcgen.Queries, ord sqlcgen.Order, rf sqlcgen.Refund) error {
	if rf.Amount != ord.Total {
		return fmt.Errorf("refund order resale harus penuh (%d != %d)", rf.Amount, ord.Total)
	}
	_, err := r.s.Ledger.Post(ctx, q, "refund", rf.ID, "refund resale", []LedgerEntry{
		{Kind: AcctResalePayable, Debit: ord.Subtotal},
		{Kind: AcctPlatformRevenue, Debit: ord.Fee},
		{Kind: AcctPaymentClearing, Credit: ord.Total},
	})
	return err
}

// ---------- pembayaran ke penjual (manual terkontrol) ----------

type SellerPayoutView struct {
	ID        string     `json:"id"`
	ListingID string     `json:"listing_id"`
	Amount    int64      `json:"amount"`
	Status    string     `json:"status"`
	BankCode  *string    `json:"bank_code,omitempty"`
	Account   *string    `json:"account_name,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`
}

func (r *ResaleService) MyPayouts(ctx context.Context, userID string) ([]SellerPayoutView, error) {
	rows, err := r.s.Q.ListSellerPayouts(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SellerPayoutView, 0, len(rows))
	for _, x := range rows {
		out = append(out, SellerPayoutView{ID: x.ID, ListingID: x.ListingID, Amount: x.Amount, Status: x.Status, BankCode: x.BankCode, Account: x.AccountName, CreatedAt: x.CreatedAt, PaidAt: x.PaidAt})
	}
	return out, nil
}

func (r *ResaleService) SetAccount(ctx context.Context, userID, payoutID, bankCode, accountName, accountNo string) error {
	bankCode, accountNo, accountName = normalizeBank(bankCode, accountNo, accountName)
	if !bankCodeRe.MatchString(bankCode) || !acctRe.MatchString(accountNo) || len([]rune(accountName)) < 2 {
		return apperr.Validation("bank_code, account_no (6–20 digit), dan account_name wajib valid")
	}
	enc, err := r.s.Box.Encrypt([]byte(accountNo))
	if err != nil {
		return err
	}
	n, err := r.s.Q.SetResalePayoutAccount(ctx, sqlcgen.SetResalePayoutAccountParams{ID: payoutID, BankCode: &bankCode, AccountName: &accountName, AccountEnc: enc, SellerID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("pembayaran")
	}
	return nil
}

type FinanceSellerPayout struct {
	SellerPayoutView
	AccountNo string `json:"account_no,omitempty"`
}

func (r *ResaleService) FinanceList(ctx context.Context, status string, limit, offset int) ([]FinanceSellerPayout, error) {
	rows, err := r.s.Q.ListResalePayoutsFinance(ctx, sqlcgen.ListResalePayoutsFinanceParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]FinanceSellerPayout, 0, len(rows))
	for _, x := range rows {
		f := FinanceSellerPayout{SellerPayoutView: SellerPayoutView{ID: x.ID, ListingID: x.ListingID, Amount: x.Amount, Status: x.Status, BankCode: x.BankCode, Account: x.AccountName, CreatedAt: x.CreatedAt, PaidAt: x.PaidAt}}
		if len(x.AccountEnc) > 0 {
			if b, err := r.s.Box.Decrypt(x.AccountEnc); err == nil {
				f.AccountNo = string(b)
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func (r *ResaleService) MarkPaid(ctx context.Context, p *auth.Principal, payoutID string, proof []byte) error {
	if !p.MFAPassed {
		return apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	if len(proof) == 0 {
		return apperr.Validation("bukti transfer wajib diunggah").With("field", "proof")
	}
	return r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		po, err := q.GetResalePayoutForUpdate(ctx, payoutID)
		if err != nil {
			return apperr.NotFound("pembayaran")
		}
		if po.Status != "ready" {
			return apperr.Conflict(apperr.CodeInvalidState, "penjual belum mengisi rekening atau sudah dibayar")
		}
		key := "resale-proof/" + po.ID
		if err := r.s.Store.Put(ctx, key, bytes.NewReader(proof), int64(len(proof)), "application/octet-stream"); err != nil {
			return err
		}
		if _, err := r.s.Ledger.Post(ctx, q, "resale_payout", po.ID, "pembayaran ke penjual resale", []LedgerEntry{
			{Kind: AcctResalePayable, Debit: po.Amount}, {Kind: AcctBankCash, Credit: po.Amount}}); err != nil {
			return err
		}
		if err := q.MarkResalePayoutPaid(ctx, sqlcgen.MarkResalePayoutPaidParams{ID: po.ID, ProofKey: &key, ProcessedBy: &p.UserID, PaidAt: ptr(r.s.now())}); err != nil {
			return err
		}
		return r.s.Audit.Log(ctx, q, p, "resale.payout_paid", "resale_payout", po.ID, nil, map[string]any{"amount": po.Amount})
	})
}
