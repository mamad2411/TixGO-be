package service

import (
	"encoding/json"
	"fmt"
	"time"

	"tixgo/backend/internal/apperr"
)

// RefundPolicy = kebijakan refund per event (docs/22 §2). Default platform: non_refundable.
type RefundPolicy struct {
	Type            string     `json:"type"` // non_refundable | refundable
	RefundableUntil *time.Time `json:"refundable_until,omitempty"`
	DeductionPct    int        `json:"deduction_pct,omitempty"`
	FeeRefundable   bool       `json:"fee_refundable"`
	OnReschedule    struct {
		WindowDays int `json:"window_days"`
	} `json:"on_reschedule"`
}

func DefaultRefundPolicy(rescheduleWindowDays int) RefundPolicy {
	var p RefundPolicy
	p.Type = "non_refundable"
	p.OnReschedule.WindowDays = rescheduleWindowDays
	return p
}

func ParseRefundPolicy(raw []byte, defaultWindow int) RefundPolicy {
	p := DefaultRefundPolicy(defaultWindow)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	if p.OnReschedule.WindowDays <= 0 {
		p.OnReschedule.WindowDays = defaultWindow
	}
	return p
}

// Validate memeriksa kebijakan terhadap waktu mulai event.
func (p RefundPolicy) Validate(startsAt time.Time) error {
	switch p.Type {
	case "non_refundable":
		if p.RefundableUntil != nil || p.DeductionPct != 0 {
			return apperr.Validation("kebijakan non_refundable tidak boleh memuat refundable_until/deduction_pct").With("field", "refund_policy")
		}
	case "refundable":
		if p.RefundableUntil == nil {
			return apperr.Validation("refundable_until wajib untuk kebijakan refundable").With("field", "refund_policy.refundable_until")
		}
		if p.RefundableUntil.After(startsAt) {
			return apperr.Validation("refundable_until tidak boleh setelah acara mulai").With("field", "refund_policy.refundable_until")
		}
		if p.DeductionPct < 0 || p.DeductionPct > 100 {
			return apperr.Validation("deduction_pct 0–100").With("field", "refund_policy.deduction_pct")
		}
	default:
		return apperr.Validation("refund_policy.type harus non_refundable atau refundable").With("field", "refund_policy.type")
	}
	if p.OnReschedule.WindowDays < 1 || p.OnReschedule.WindowDays > 90 {
		return apperr.Validation("on_reschedule.window_days 1–90").With("field", "refund_policy.on_reschedule.window_days")
	}
	return nil
}

func (p RefundPolicy) JSON() []byte {
	b, _ := json.Marshal(p)
	return b
}

// Summary = ringkasan satu kalimat untuk halaman event, checkout, dan email (BR-13: tampil sebelum bayar).
func (p RefundPolicy) Summary(locale string) string {
	en := locale == "en"
	if p.Type == "non_refundable" {
		if en {
			return fmt.Sprintf("Tickets are non-refundable, except if the event is cancelled (full refund). If rescheduled, you can request a refund within %d days of the announcement.", p.OnReschedule.WindowDays)
		}
		return fmt.Sprintf("Tiket tidak dapat dikembalikan, kecuali acara dibatalkan (refund penuh). Jika acara dijadwalkan ulang, kamu dapat meminta refund dalam %d hari sejak pengumuman.", p.OnReschedule.WindowDays)
	}
	until := ""
	if p.RefundableUntil != nil {
		until = p.RefundableUntil.UTC().Format("02 Jan 2006")
	}
	fee := "biaya layanan tidak dikembalikan"
	if p.FeeRefundable {
		fee = "biaya layanan ikut dikembalikan"
	}
	if en {
		return fmt.Sprintf("Refundable until %s with a %d%% deduction; service fee %s. Cancelled events are refunded in full.", until, p.DeductionPct, map[bool]string{true: "is refunded", false: "is non-refundable"}[p.FeeRefundable])
	}
	return fmt.Sprintf("Dapat di-refund sampai %s dengan potongan %d%%; %s. Acara dibatalkan = refund penuh.", until, p.DeductionPct, fee)
}
