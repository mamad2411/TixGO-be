package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// OrganizerService = onboarding & KYB organizer, rekening, tim (docs/20 M33).
type OrganizerService struct{ s *Services }

func newOrganizerService(s *Services) *OrganizerService { return &OrganizerService{s: s} }

// OrgScope = organizer + peran pemanggil di dalamnya (diisi middleware).
type OrgScope struct {
	OrganizerID string
	Role        string // owner|member|gate_staff
}

func (o OrgScope) IsOwner() bool { return o.Role == "owner" }

type OrganizerView struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Type               string     `json:"type"`
	LegalName          *string    `json:"legal_name"`
	ContactEmail       *string    `json:"contact_email"`
	ContactPhone       *string    `json:"contact_phone"`
	Address            *string    `json:"address"`
	KYBStatus          string     `json:"kyb_status"`
	KYBReason          *string    `json:"kyb_reason,omitempty"`
	ContractAcceptedAt *time.Time `json:"contract_accepted_at"`
	Status             string     `json:"status"`
	CanSellPaid        bool       `json:"can_sell_paid"`
	CreatedAt          time.Time  `json:"created_at"`
}

func orgView(o sqlcgen.Organizer) OrganizerView {
	return OrganizerView{ID: o.ID, Name: o.Name, Type: o.Type, LegalName: o.LegalName, ContactEmail: o.ContactEmail,
		ContactPhone: o.ContactPhone, Address: o.Address, KYBStatus: o.KybStatus, KYBReason: o.KybReason,
		ContractAcceptedAt: o.ContractAcceptedAt, Status: o.Status, CanSellPaid: o.KybStatus == "approved" && o.Status == "active", CreatedAt: o.CreatedAt}
}

type ApplyReq struct {
	Name         string `json:"name" binding:"required,max=120"`
	Type         string `json:"type" binding:"omitempty,oneof=individual company"`
	LegalName    string `json:"legal_name" binding:"omitempty,max=200"`
	ContactEmail string `json:"contact_email" binding:"omitempty,max=254"`
	ContactPhone string `json:"contact_phone" binding:"omitempty,max=32"`
	Address      string `json:"address" binding:"omitempty,max=500"`
}

// Apply membuat organizer untuk pengguna login (satu pengguna = satu organizer).
func (o *OrganizerService) Apply(ctx context.Context, p *auth.Principal, r ApplyReq) (*OrganizerView, error) {
	name := strings.TrimSpace(r.Name)
	if l := len([]rune(name)); l < 3 || l > 120 {
		return nil, apperr.Validation("nama organizer 3–120 karakter").With("field", "name")
	}
	typ := r.Type
	if typ == "" {
		typ = "individual"
	}
	if typ != "individual" && typ != "company" {
		return nil, apperr.Validation("type harus individual atau company").With("field", "type")
	}
	var out sqlcgen.Organizer
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		u, err := q.GetUserByIDForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		if u.Status != "active" {
			return apperr.ForbiddenCode(apperr.CodeBlocked, "akun tidak aktif")
		}
		if u.EmailVerifiedAt == nil {
			return apperr.ForbiddenCode(apperr.CodeEmailNotVerified, "verifikasi email diperlukan")
		}
		if u.Role != "customer" {
			return apperr.Conflict(apperr.CodeConflict, "peran akun ini tidak dapat mendaftar sebagai organizer")
		}
		if _, err := q.GetMembership(ctx, p.UserID); err == nil {
			return apperr.Conflict(apperr.CodeConflict, "akun sudah terhubung dengan organizer")
		}
		var contactEmail *string
		if r.ContactEmail != "" {
			e, ok := NormalizeEmail(r.ContactEmail)
			if !ok {
				return apperr.Validation("email kontak tidak valid").With("field", "contact_email")
			}
			contactEmail = &e
		} else {
			contactEmail = &u.Email
		}
		out, err = q.CreateOrganizer(ctx, sqlcgen.CreateOrganizerParams{
			ID: id.New(), Name: name, OwnerUserID: p.UserID, Type: typ,
			LegalName: strPtr(r.LegalName), ContactEmail: contactEmail, ContactPhone: strPtr(r.ContactPhone), Address: strPtr(r.Address),
		})
		if err != nil {
			return err
		}
		if err := q.AddOrganizerMember(ctx, sqlcgen.AddOrganizerMemberParams{OrganizerID: out.ID, UserID: p.UserID, Role: "owner"}); err != nil {
			return err
		}
		if err := q.SetUserRole(ctx, sqlcgen.SetUserRoleParams{ID: p.UserID, Role: "organizer"}); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.apply", "organizer", out.ID, nil, map[string]any{"name": name})
	})
	if err != nil {
		return nil, err
	}
	v := orgView(out)
	return &v, nil
}

func (o *OrganizerService) Get(ctx context.Context, orgID string) (*OrganizerView, error) {
	org, err := o.s.Q.GetOrganizer(ctx, orgID)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("organizer")
		}
		return nil, err
	}
	v := orgView(org)
	return &v, nil
}

type OrgPatch struct {
	Name         *string `json:"name" binding:"omitempty,max=120"`
	Type         *string `json:"type" binding:"omitempty,oneof=individual company"`
	LegalName    *string `json:"legal_name" binding:"omitempty,max=200"`
	ContactEmail *string `json:"contact_email" binding:"omitempty,max=254"`
	ContactPhone *string `json:"contact_phone" binding:"omitempty,max=32"`
	Address      *string `json:"address" binding:"omitempty,max=500"`
	TaxID        *string `json:"tax_id" binding:"omitempty,max=40"`
}

func (o *OrganizerService) Update(ctx context.Context, p *auth.Principal, sc OrgScope, r OrgPatch) (*OrganizerView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	arg := sqlcgen.UpdateOrganizerInfoParams{ID: sc.OrganizerID, Name: r.Name, Type: r.Type, LegalName: r.LegalName, ContactPhone: r.ContactPhone, Address: r.Address}
	if r.Name != nil {
		if l := len([]rune(strings.TrimSpace(*r.Name))); l < 3 || l > 120 {
			return nil, apperr.Validation("nama organizer 3–120 karakter").With("field", "name")
		}
	}
	if r.Type != nil && *r.Type != "individual" && *r.Type != "company" {
		return nil, apperr.Validation("type harus individual atau company").With("field", "type")
	}
	if r.ContactEmail != nil {
		e, ok := NormalizeEmail(*r.ContactEmail)
		if !ok {
			return nil, apperr.Validation("email kontak tidak valid").With("field", "contact_email")
		}
		arg.ContactEmail = &e
	}
	if r.TaxID != nil && *r.TaxID != "" {
		enc, err := o.s.Box.Encrypt([]byte(*r.TaxID))
		if err != nil {
			return nil, err
		}
		arg.TaxIDEnc = enc
	}
	var out sqlcgen.Organizer
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.UpdateOrganizerInfo(ctx, arg)
		if err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.update", "organizer", out.ID, nil, map[string]any{"fields": "info"})
	})
	if err != nil {
		return nil, err
	}
	v := orgView(out)
	return &v, nil
}

// ---- dokumen KYB ----

var allowedDocTypes = map[string]string{"application/pdf": ".pdf", "image/jpeg": ".jpg", "image/png": ".png"}

const maxDocBytes = 5 << 20

type DocView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

var docKinds = map[string]bool{"ktp": true, "npwp": true, "akta": true, "nib": true, "siup": true, "other": true}

func (o *OrganizerService) UploadDocument(ctx context.Context, p *auth.Principal, sc OrgScope, kind string, r io.Reader) (*DocView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	if !docKinds[kind] {
		return nil, apperr.Validation("jenis dokumen tidak dikenal").With("field", "kind")
	}
	org, err := o.s.Q.GetOrganizer(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	if org.KybStatus == "submitted" || org.KybStatus == "in_review" || org.KybStatus == "approved" {
		return nil, apperr.Conflict(apperr.CodeInvalidState, "dokumen tidak dapat diubah pada status "+org.KybStatus)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxDocBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocBytes {
		return nil, apperr.Validation("dokumen maksimal 5 MB")
	}
	ct := http.DetectContentType(data) // sniff isi, bukan header klien
	ext, ok := allowedDocTypes[ct]
	if !ok {
		return nil, apperr.Validation("dokumen harus PDF, JPEG, atau PNG").With("detected", ct)
	}
	docID := id.New()
	key := fmt.Sprintf("kyb/%s/%s%s", sc.OrganizerID, docID, ext)
	if err := o.s.Store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), ct); err != nil {
		return nil, err
	}
	err = o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.InsertOrganizerDocument(ctx, sqlcgen.InsertOrganizerDocumentParams{ID: docID, OrganizerID: sc.OrganizerID, Kind: kind, ObjectKey: key}); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.document.upload", "organizer", sc.OrganizerID, nil, map[string]any{"kind": kind})
	})
	if err != nil {
		_ = o.s.Store.Delete(ctx, key)
		return nil, err
	}
	return &DocView{ID: docID, Kind: kind, CreatedAt: o.s.now()}, nil
}

func (o *OrganizerService) Documents(ctx context.Context, orgID string) ([]DocView, error) {
	rows, err := o.s.Q.ListOrganizerDocuments(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]DocView, 0, len(rows))
	for _, r := range rows {
		out = append(out, DocView{ID: r.ID, Kind: r.Kind, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (o *OrganizerService) DeleteDocument(ctx context.Context, p *auth.Principal, sc OrgScope, docID string) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	org, err := o.s.Q.GetOrganizer(ctx, sc.OrganizerID)
	if err != nil {
		return err
	}
	if org.KybStatus == "submitted" || org.KybStatus == "in_review" || org.KybStatus == "approved" {
		return apperr.Conflict(apperr.CodeInvalidState, "dokumen tidak dapat dihapus pada status "+org.KybStatus)
	}
	d, err := o.s.Q.GetOrganizerDocument(ctx, sqlcgen.GetOrganizerDocumentParams{ID: docID, OrganizerID: sc.OrganizerID})
	if err != nil {
		return apperr.NotFound("dokumen")
	}
	if err := o.s.Q.DeleteOrganizerDocument(ctx, sqlcgen.DeleteOrganizerDocumentParams{ID: docID, OrganizerID: sc.OrganizerID}); err != nil {
		return err
	}
	return o.s.Store.Delete(ctx, d.ObjectKey)
}

// ---- rekening ----

type BankView struct {
	ID         string     `json:"id"`
	BankCode   string     `json:"bank_code"`
	AccountNo  string     `json:"account_no_masked"`
	Name       string     `json:"account_name"`
	Verified   bool       `json:"verified"`
	ActiveFrom *time.Time `json:"active_from"`
	Usable     bool       `json:"usable"`
}

var (
	bankCodeRe = regexp.MustCompile(`^[A-Za-z0-9_]{2,16}$`)
	acctNoRe   = regexp.MustCompile(`^[0-9]{6,20}$`)
)

const bankChangeCooldown = 3 * 24 * time.Hour // M33 AC4 (T)

func (o *OrganizerService) bankView(b sqlcgen.BankAccount) BankView {
	return BankView{ID: b.ID, BankCode: b.BankCode, AccountNo: "****" + b.AccountNoLast4, Name: b.AccountName,
		Verified: b.VerifiedAt != nil, ActiveFrom: b.ActiveFrom, Usable: b.Active && b.VerifiedAt != nil && b.ActiveFrom != nil && !b.ActiveFrom.After(o.s.now())}
}

func (o *OrganizerService) AddBankAccount(ctx context.Context, p *auth.Principal, sc OrgScope, bankCode, accountNo, accountName string) (*BankView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan untuk rekening").With("setup", true)
	}
	bankCode = strings.ToUpper(strings.TrimSpace(bankCode))
	accountNo = strings.NewReplacer(" ", "", "-", "").Replace(accountNo)
	accountName = strings.TrimSpace(accountName)
	if !bankCodeRe.MatchString(bankCode) {
		return nil, apperr.Validation("kode bank tidak valid").With("field", "bank_code")
	}
	if !acctNoRe.MatchString(accountNo) {
		return nil, apperr.Validation("nomor rekening 6–20 digit").With("field", "account_no")
	}
	if l := len([]rune(accountName)); l < 2 || l > 100 {
		return nil, apperr.Validation("nama pemilik rekening 2–100 karakter").With("field", "account_name")
	}
	org, err := o.s.Q.GetOrganizer(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	enc, err := o.s.Box.Encrypt([]byte(accountNo))
	if err != nil {
		return nil, err
	}
	activeFrom := o.s.now()
	if org.KybStatus == "approved" {
		activeFrom = activeFrom.Add(bankChangeCooldown) // masa tunggu setelah perubahan rekening pada organizer aktif
	}
	var out sqlcgen.BankAccount
	err = o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		existing, err := q.ListBankAccounts(ctx, sc.OrganizerID)
		if err != nil {
			return err
		}
		if len(existing) >= 5 {
			return apperr.Conflict(apperr.CodeLimitExceeded, "maksimal 5 rekening aktif")
		}
		out, err = q.InsertBankAccount(ctx, sqlcgen.InsertBankAccountParams{
			ID: id.New(), OrganizerID: sc.OrganizerID, BankCode: bankCode, AccountNoEnc: enc,
			AccountNoLast4: accountNo[len(accountNo)-4:], AccountName: accountName, ActiveFrom: &activeFrom,
		})
		if err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.bank.add", "bank_account", out.ID, nil, map[string]any{"bank": bankCode, "last4": out.AccountNoLast4})
	})
	if err != nil {
		return nil, err
	}
	v := o.bankView(out)
	return &v, nil
}

func (o *OrganizerService) BankAccounts(ctx context.Context, orgID string) ([]BankView, error) {
	rows, err := o.s.Q.ListBankAccounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]BankView, 0, len(rows))
	for _, r := range rows {
		out = append(out, o.bankView(r))
	}
	return out, nil
}

func (o *OrganizerService) RemoveBankAccount(ctx context.Context, p *auth.Principal, sc OrgScope, bankID string) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	if !p.MFAPassed {
		return apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	return o.s.Q.DeactivateBankAccount(ctx, sqlcgen.DeactivateBankAccountParams{ID: bankID, OrganizerID: sc.OrganizerID})
}

// VerifyBank dipakai finance/admin untuk rekening yang ditambahkan setelah organizer disetujui.
func (o *OrganizerService) VerifyBank(ctx context.Context, p *auth.Principal, bankID string) error {
	b, err := o.s.Q.GetBankAccount(ctx, bankID)
	if err != nil {
		return apperr.NotFound("rekening")
	}
	return o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.VerifyBankAccount(ctx, bankID); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "finance.bank.verify", "bank_account", bankID, nil, map[string]any{"organizer": b.OrganizerID})
	})
}

// ---- kontrak & pengajuan ----

func (o *OrganizerService) AcceptContract(ctx context.Context, p *auth.Principal, sc OrgScope, documentID string) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	d, err := o.s.Q.GetLegalByID(ctx, documentID)
	if err != nil || d.Kind != "organizer_terms" || d.PublishedAt == nil {
		return apperr.Validation("dokumen ketentuan organizer tidak valid").With("field", "document_id")
	}
	latest, err := o.s.Q.LatestPublishedLegal(ctx, sqlcgen.LatestPublishedLegalParams{Kind: "organizer_terms", Locale: d.Locale})
	if err == nil && latest.Version > d.Version {
		return apperr.Conflict(apperr.CodeConsentRequired, "ada versi ketentuan organizer yang lebih baru").With("document_id", latest.ID)
	}
	return o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.AcceptOrganizerContract(ctx, sqlcgen.AcceptOrganizerContractParams{ID: sc.OrganizerID, ContractDocumentID: &documentID}); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.contract.accept", "organizer", sc.OrganizerID, nil, map[string]any{"document": documentID, "version": d.Version})
	})
}

func (o *OrganizerService) SubmitKYB(ctx context.Context, p *auth.Principal, sc OrgScope) (*OrganizerView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	org, err := o.s.Q.GetOrganizer(ctx, sc.OrganizerID)
	if err != nil {
		return nil, err
	}
	var missing []string
	if org.LegalName == nil || strings.TrimSpace(*org.LegalName) == "" {
		missing = append(missing, "legal_name")
	}
	if org.ContactPhone == nil || strings.TrimSpace(*org.ContactPhone) == "" {
		missing = append(missing, "contact_phone")
	}
	if org.Address == nil || strings.TrimSpace(*org.Address) == "" {
		missing = append(missing, "address")
	}
	if docs, _ := o.s.Q.ListOrganizerDocuments(ctx, org.ID); len(docs) == 0 {
		missing = append(missing, "documents")
	}
	if banks, _ := o.s.Q.ListBankAccounts(ctx, org.ID); len(banks) == 0 {
		missing = append(missing, "bank_account")
	}
	if _, err := o.s.Q.LatestPublishedLegal(ctx, sqlcgen.LatestPublishedLegalParams{Kind: "organizer_terms", Locale: "id"}); err == nil && org.ContractAcceptedAt == nil {
		missing = append(missing, "contract")
	}
	if len(missing) > 0 {
		return nil, apperr.Unprocessable(apperr.CodeValidation, "data KYB belum lengkap").With("missing", missing)
	}
	var out sqlcgen.Organizer
	err = o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.SubmitKYB(ctx, org.ID)
		if err != nil {
			if platform.IsNoRows(err) {
				return apperr.Conflict(apperr.CodeInvalidState, "status KYB saat ini tidak dapat diajukan: "+org.KybStatus)
			}
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.kyb.submit", "organizer", org.ID, map[string]any{"from": org.KybStatus}, map[string]any{"to": "submitted"})
	})
	if err != nil {
		return nil, err
	}
	v := orgView(out)
	return &v, nil
}

// ---- Admin: tinjauan KYB ----

type KYBItem struct {
	OrganizerView
	OwnerUserID string     `json:"owner_user_id"`
	Documents   []DocView  `json:"documents,omitempty"`
	Banks       []BankView `json:"banks,omitempty"`
}

func (o *OrganizerService) AdminList(ctx context.Context, status string, limit, offset int) ([]OrganizerView, int, error) {
	var st *string
	if status != "" {
		st = &status
	}
	rows, err := o.s.Q.ListOrganizersByKYB(ctx, sqlcgen.ListOrganizersByKYBParams{Status: st, Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := o.s.Q.CountOrganizersByKYB(ctx, st)
	if err != nil {
		return nil, 0, err
	}
	out := make([]OrganizerView, 0, len(rows))
	for _, r := range rows {
		out = append(out, orgView(r))
	}
	return out, int(n), nil
}

func (o *OrganizerService) AdminGet(ctx context.Context, orgID string) (*KYBItem, error) {
	org, err := o.s.Q.GetOrganizer(ctx, orgID)
	if err != nil {
		return nil, apperr.NotFound("organizer")
	}
	docs, _ := o.Documents(ctx, orgID)
	banks, _ := o.BankAccounts(ctx, orgID)
	if org.KybStatus == "submitted" {
		_ = o.s.Q.SetOrganizerInReview(ctx, orgID)
		org.KybStatus = "in_review"
	}
	return &KYBItem{OrganizerView: orgView(org), OwnerUserID: org.OwnerUserID, Documents: docs, Banks: banks}, nil
}

// DocumentURL memberi URL sementara (presigned) untuk dokumen KYB — hanya untuk staf yang berwenang.
func (o *OrganizerService) DocumentURL(ctx context.Context, p *auth.Principal, orgID, docID string) (string, error) {
	d, err := o.s.Q.GetOrganizerDocument(ctx, sqlcgen.GetOrganizerDocumentParams{ID: docID, OrganizerID: orgID})
	if err != nil {
		return "", apperr.NotFound("dokumen")
	}
	u, err := o.s.Store.PresignGet(ctx, d.ObjectKey, 5*time.Minute)
	if err != nil {
		return "", err
	}
	// akses dokumen sensitif oleh staf tercatat
	_ = o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return o.s.Audit.Log(ctx, q, p, "kyb.document.view", "organizer_document", docID, nil, map[string]any{"organizer": orgID})
	})
	return u, nil
}

func (o *OrganizerService) Review(ctx context.Context, p *auth.Principal, orgID, decision, reason string) (*OrganizerView, error) {
	status := map[string]string{"approve": "approved", "reject": "rejected", "needs_info": "needs_info"}[decision]
	if status == "" {
		return nil, apperr.Validation("decision harus approve, reject, atau needs_info").With("field", "decision")
	}
	if status != "approved" && strings.TrimSpace(reason) == "" {
		return nil, apperr.Validation("alasan wajib diisi").With("field", "reason")
	}
	var out sqlcgen.Organizer
	var owner sqlcgen.User
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.ReviewKYB(ctx, sqlcgen.ReviewKYBParams{ID: orgID, KybStatus: status, Reason: strPtr(reason), KybReviewedBy: strPtr(p.UserID)})
		if err != nil {
			if platform.IsNoRows(err) {
				return apperr.Conflict(apperr.CodeInvalidState, "organizer tidak dalam status yang dapat ditinjau")
			}
			return err
		}
		if status == "approved" {
			if err := q.VerifyOrganizerBankAccounts(ctx, orgID); err != nil {
				return err
			}
		}
		owner, err = q.GetUserByID(ctx, out.OwnerUserID)
		if err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "kyb.review", "organizer", orgID, nil, map[string]any{"decision": decision, "reason": reason})
	})
	if err != nil {
		return nil, err
	}
	o.s.Notifier.Send(ctx, NotifyReq{UserID: owner.ID, Template: "kyb_update",
		Data: map[string]any{"name": owner.Name, "organizer": out.Name, "status": status, "reason": reason}})
	v := orgView(out)
	return &v, nil
}

// SetSuspended menangguhkan/mengaktifkan organizer (admin).
func (o *OrganizerService) SetSuspended(ctx context.Context, p *auth.Principal, orgID string, suspended bool) error {
	st := "active"
	if suspended {
		st = "suspended"
	}
	return o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.SetOrganizerStatus(ctx, sqlcgen.SetOrganizerStatusParams{ID: orgID, Status: st}); err != nil {
			return err
		}
		// penjualan seluruh event terbit dihentikan (tiket yang sudah terjual tetap sah); saat diaktifkan kembali hanya yang dijeda oleh penangguhan yang dilanjutkan
		if suspended {
			if err := q.PauseOrgEventsForSuspension(ctx, orgID); err != nil {
				return err
			}
		} else if err := q.ResumeOrgEventsAfterSuspension(ctx, orgID); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.status", "organizer", orgID, nil, map[string]any{"status": st})
	})
}

// ---- Tim ----

type MemberView struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func (o *OrganizerService) Team(ctx context.Context, orgID string) ([]MemberView, error) {
	rows, err := o.s.Q.ListOrganizerMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(rows))
	for _, r := range rows {
		out = append(out, MemberView{UserID: r.UserID, Email: r.Email, Name: r.Name, Role: r.Role, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// AddMember menambahkan pengguna yang SUDAH terdaftar (berdasarkan email) ke tim.
func (o *OrganizerService) AddMember(ctx context.Context, p *auth.Principal, sc OrgScope, email, role string) (*MemberView, error) {
	if !sc.IsOwner() {
		return nil, apperr.Forbidden("hanya pemilik organizer")
	}
	if !p.MFAPassed {
		return nil, apperr.ForbiddenCode(apperr.CodeMFARequired, "verifikasi dua langkah diperlukan").With("setup", true)
	}
	if role != "member" && role != "gate_staff" {
		return nil, apperr.Validation("role harus member atau gate_staff").With("field", "role")
	}
	em, ok := NormalizeEmail(email)
	if !ok {
		return nil, apperr.Validation("email tidak valid").With("field", "email")
	}
	var mv MemberView
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		u, err := q.GetUserByEmailForTeam(ctx, em)
		if err != nil {
			return apperr.NotFound("pengguna dengan email itu (minta mereka mendaftar dulu)")
		}
		if u.Role != "customer" {
			return apperr.Conflict(apperr.CodeConflict, "pengguna ini tidak dapat ditambahkan ke tim")
		}
		if _, err := q.GetMembership(ctx, u.ID); err == nil {
			return apperr.Conflict(apperr.CodeConflict, "pengguna sudah tergabung di organizer")
		}
		if err := q.AddOrganizerMember(ctx, sqlcgen.AddOrganizerMemberParams{OrganizerID: sc.OrganizerID, UserID: u.ID, Role: role}); err != nil {
			return err
		}
		userRole := "organizer_member"
		if role == "gate_staff" {
			userRole = "gate_staff"
		}
		if err := q.SetUserRole(ctx, sqlcgen.SetUserRoleParams{ID: u.ID, Role: userRole}); err != nil {
			return err
		}
		mv = MemberView{UserID: u.ID, Email: u.Email, Name: u.Name, Role: role, CreatedAt: o.s.now()}
		return o.s.Audit.Log(ctx, q, p, "organizer.team.add", "organizer", sc.OrganizerID, nil, map[string]any{"user": u.ID, "role": role})
	})
	if err != nil {
		return nil, err
	}
	return &mv, nil
}

func (o *OrganizerService) RemoveMember(ctx context.Context, p *auth.Principal, sc OrgScope, userID string) error {
	if !sc.IsOwner() {
		return apperr.Forbidden("hanya pemilik organizer")
	}
	if userID == p.UserID {
		return apperr.Conflict(apperr.CodeConflict, "pemilik tidak dapat menghapus dirinya")
	}
	err := o.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.RemoveOrganizerMember(ctx, sqlcgen.RemoveOrganizerMemberParams{OrganizerID: sc.OrganizerID, UserID: userID}); err != nil {
			return err
		}
		if err := q.SetUserRole(ctx, sqlcgen.SetUserRoleParams{ID: userID, Role: "customer"}); err != nil {
			return err
		}
		return o.s.Audit.Log(ctx, q, p, "organizer.team.remove", "organizer", sc.OrganizerID, nil, map[string]any{"user": userID})
	})
	if err != nil {
		return err
	}
	o.s.Auth.RevokeUserAccess(ctx, userID) // peran lama tidak boleh dipakai lagi
	return nil
}

// Membership dipakai middleware untuk mengisi OrgScope.
func (o *OrganizerService) Membership(ctx context.Context, userID string) (*OrgScope, string, error) {
	m, err := o.s.Q.GetMembership(ctx, userID)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if m.OrganizerStatus != "active" {
		return nil, "suspended", nil
	}
	return &OrgScope{OrganizerID: m.OrganizerID, Role: m.Role}, m.KybStatus, nil
}
