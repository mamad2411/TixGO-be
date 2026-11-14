// Package apperr = error terstandar API (docs/08 §1-§2, §14). Handler memetakan *Error ke respons
// {"error":{"code","message","details"}}; error lain menjadi 500 tanpa membocorkan detail.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Kode error (docs/08 §2 dan §14).
const (
	CodeValidation          = "VALIDATION_FAILED"
	CodeUnauthenticated     = "UNAUTHENTICATED"
	CodeForbidden           = "FORBIDDEN"
	CodeNotFound            = "NOT_FOUND"
	CodePhoneTaken          = "PHONE_TAKEN"
	CodeEmailTaken          = "EMAIL_TAKEN"
	CodeOTPInvalid          = "OTP_INVALID"
	CodeOTPLocked           = "OTP_LOCKED"
	CodeRateLimited         = "RATE_LIMITED"
	CodeNotAdmitted         = "NOT_ADMITTED"
	CodeQueueClosed         = "QUEUE_CLOSED"
	CodeAdmissionExpired    = "ADMISSION_EXPIRED"
	CodeSeatTaken           = "SEAT_TAKEN"
	CodeHoldExpired         = "HOLD_EXPIRED"
	CodeLimitExceeded       = "LIMIT_EXCEEDED"
	CodeSaleNotOpen         = "SALE_NOT_OPEN"
	CodeSoldOut             = "SOLD_OUT"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodePaymentFailed       = "PAYMENT_FAILED"
	CodeOrderNotPayable     = "ORDER_NOT_PAYABLE"
	CodeTicketInvalid       = "TICKET_INVALID"
	CodeTicketUsed          = "TICKET_USED"
	CodeRefundNotAllowed    = "REFUND_NOT_ALLOWED"
	CodePresaleCodeInvalid  = "PRESALE_CODE_INVALID"

	CodeEmailNotVerified  = "EMAIL_NOT_VERIFIED"
	CodeTokenInvalid      = "TOKEN_INVALID"
	CodeTokenExpired      = "TOKEN_EXPIRED"
	CodeConsentRequired   = "CONSENT_REQUIRED"
	CodeKYBNotApproved    = "KYB_NOT_APPROVED"
	CodePromoInvalid      = "PROMO_INVALID"
	CodePromoExhausted    = "PROMO_EXHAUSTED"
	CodeSalePaused        = "SALE_PAUSED"
	CodeInsufficientFunds = "INSUFFICIENT_BALANCE"
	CodeAgeConfirmation   = "AGE_CONFIRMATION_REQUIRED"
	CodeMFARequired       = "MFA_REQUIRED"
	CodeDSRPending        = "DSR_PENDING"
	CodeInvalidState      = "INVALID_STATE"
	CodeConflict          = "CONFLICT"
	CodeNotAllowed        = "NOT_ALLOWED"
	CodeMaintenance       = "MAINTENANCE"
	CodeAccountLocked     = "ACCOUNT_LOCKED"
	CodeInvalidCredential = "INVALID_CREDENTIALS"
	CodeBlocked           = "BLOCKED"
	CodeTooLarge          = "PAYLOAD_TOO_LARGE"
)

// Error adalah error domain yang aman ditampilkan ke klien.
type Error struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Err     error // penyebab internal, tidak pernah dikirim ke klien
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// With mengembalikan salinan dengan detail tambahan.
func (e *Error) With(key string, value any) *Error {
	cp := *e
	cp.Details = make(map[string]any, len(e.Details)+1)
	for k, v := range e.Details {
		cp.Details[k] = v
	}
	cp.Details[key] = value
	return &cp
}

// Wrap melampirkan penyebab internal.
func (e *Error) Wrap(err error) *Error {
	cp := *e
	cp.Err = err
	return &cp
}

func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func BadRequest(code, msg string) *Error    { return New(http.StatusBadRequest, code, msg) }
func Validation(msg string) *Error          { return New(http.StatusBadRequest, CodeValidation, msg) }
func Unauthorized(msg string) *Error        { return New(http.StatusUnauthorized, CodeUnauthenticated, msg) }
func Forbidden(msg string) *Error           { return New(http.StatusForbidden, CodeForbidden, msg) }
func ForbiddenCode(code, msg string) *Error { return New(http.StatusForbidden, code, msg) }
func NotFound(what string) *Error {
	return New(http.StatusNotFound, CodeNotFound, what+" tidak ditemukan")
}
func Conflict(code, msg string) *Error      { return New(http.StatusConflict, code, msg) }
func Unprocessable(code, msg string) *Error { return New(http.StatusUnprocessableEntity, code, msg) }
func TooMany(msg string) *Error             { return New(http.StatusTooManyRequests, CodeRateLimited, msg) }
func Internal(err error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "INTERNAL", Message: "terjadi kesalahan pada server", Err: err}
}

// As mengekstrak *Error dari rantai error.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Is memeriksa apakah err adalah *Error dengan kode tertentu.
func Is(err error, code string) bool {
	e, ok := As(err)
	return ok && e.Code == code
}
