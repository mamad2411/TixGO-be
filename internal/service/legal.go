package service

import (
	"context"
	"strings"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/reqmeta"
)

// LegalService = dokumen legal berversi, persetujuan, cookie consent (docs/20 M28).
type LegalService struct{ s *Services }

func newLegalService(s *Services) *LegalService { return &LegalService{s: s} }

var legalKinds = map[string]bool{"terms": true, "privacy": true, "refund": true, "about": true, "faq": true, "contact": true, "how_to_buy": true, "organizer_terms": true}

type LegalDoc struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Locale      string  `json:"locale"`
	Version     int     `json:"version"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	PublishedAt *string `json:"published_at,omitempty"`
}

func legalView(d sqlcgen.LegalDocument) LegalDoc {
	v := LegalDoc{ID: d.ID, Kind: d.Kind, Locale: d.Locale, Version: d.Version, Title: d.Title, Body: d.Body}
	if d.PublishedAt != nil {
		s := d.PublishedAt.UTC().Format("2006-01-02T15:04:05Z")
		v.PublishedAt = &s
	}
	return v
}

// Active mengembalikan versi terbit terbaru; locale tidak ada → jatuh ke id.
func (l *LegalService) Active(ctx context.Context, kind, locale string) (*LegalDoc, error) {
	if !legalKinds[kind] {
		return nil, apperr.NotFound("dokumen")
	}
	if locale != "en" {
		locale = "id"
	}
	d, err := l.s.Q.LatestPublishedLegal(ctx, sqlcgen.LatestPublishedLegalParams{Kind: kind, Locale: locale})
	if err != nil && platform.IsNoRows(err) && locale != "id" {
		d, err = l.s.Q.LatestPublishedLegal(ctx, sqlcgen.LatestPublishedLegalParams{Kind: kind, Locale: "id"})
	}
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("dokumen")
		}
		return nil, err
	}
	v := legalView(d)
	return &v, nil
}

func (l *LegalService) Versions(ctx context.Context, kind, locale string) ([]sqlcgen.ListLegalVersionsRow, error) {
	if !legalKinds[kind] {
		return nil, apperr.NotFound("dokumen")
	}
	if locale != "en" {
		locale = "id"
	}
	return l.s.Q.ListLegalVersions(ctx, sqlcgen.ListLegalVersionsParams{Kind: kind, Locale: locale})
}

// recordSignupConsent mencatat persetujuan atas Syarat & Privasi terbit saat daftar. Tanpa dokumen terbit → tidak ada.
func (l *LegalService) recordSignupConsent(ctx context.Context, q *sqlcgen.Queries, userID string) error {
	docs, err := q.PendingConsentDocs(ctx, userID)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := q.RecordConsent(ctx, sqlcgen.RecordConsentParams{ID: id.New(), UserID: userID, DocumentID: d.ID, IpHash: strPtr(hashIP(ctx))}); err != nil {
			return err
		}
	}
	return nil
}

func hashIP(ctx context.Context) string {
	ip := reqmeta.From(ctx).IP
	if ip == "" {
		return ""
	}
	return auth.HashToken(ip)
}

// Pending = dokumen wajib yang belum disetujui versi terbarunya.
func (l *LegalService) Pending(ctx context.Context, q *sqlcgen.Queries, userID string) ([]sqlcgen.PendingConsentDocsRow, error) {
	return q.PendingConsentDocs(ctx, userID)
}

// RequireConsents menolak aksi (checkout) bila ada versi legal baru yang belum disetujui (T24).
func (l *LegalService) RequireConsents(ctx context.Context, q *sqlcgen.Queries, userID string) error {
	pend, err := q.PendingConsentDocs(ctx, userID)
	if err != nil {
		return err
	}
	if len(pend) == 0 {
		return nil
	}
	docs := make([]map[string]any, 0, len(pend))
	for _, p := range pend {
		docs = append(docs, map[string]any{"document_id": p.ID, "kind": p.Kind, "version": p.Version})
	}
	return apperr.ForbiddenCode(apperr.CodeConsentRequired, "setujui Syarat & Ketentuan/Kebijakan Privasi terbaru").With("documents", docs)
}

func (l *LegalService) Accept(ctx context.Context, userID, documentID string) error {
	d, err := l.s.Q.GetLegalByID(ctx, documentID)
	if err != nil {
		if platform.IsNoRows(err) {
			return apperr.NotFound("dokumen")
		}
		return err
	}
	if d.PublishedAt == nil {
		return apperr.BadRequest(apperr.CodeInvalidState, "dokumen belum terbit")
	}
	return l.s.Q.RecordConsent(ctx, sqlcgen.RecordConsentParams{ID: id.New(), UserID: userID, DocumentID: documentID, IpHash: strPtr(hashIP(ctx))})
}

func (l *LegalService) MyConsents(ctx context.Context, userID string) ([]sqlcgen.ListUserConsentsRow, error) {
	return l.s.Q.ListUserConsents(ctx, userID)
}

// ---- Admin/editor ----

func (l *LegalService) CreateDraft(ctx context.Context, p *auth.Principal, kind, locale, title, body string) (*LegalDoc, error) {
	if !legalKinds[kind] {
		return nil, apperr.Validation("jenis dokumen tidak dikenal").With("field", "kind")
	}
	if locale != "id" && locale != "en" {
		return nil, apperr.Validation("locale harus id atau en").With("field", "locale")
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return nil, apperr.Validation("judul dan isi wajib")
	}
	var out sqlcgen.LegalDocument
	err := l.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		next, err := q.NextLegalVersion(ctx, sqlcgen.NextLegalVersionParams{Kind: kind, Locale: locale})
		if err != nil {
			return err
		}
		out, err = q.InsertLegalDocument(ctx, sqlcgen.InsertLegalDocumentParams{ID: id.New(), Kind: kind, Locale: locale, Version: int(next), Title: title, Body: body, CreatedBy: strPtr(p.UserID)})
		if err != nil {
			return err
		}
		return l.s.Audit.Log(ctx, q, p, "legal.draft", "legal_document", out.ID, nil, map[string]any{"kind": kind, "locale": locale, "version": out.Version})
	})
	if err != nil {
		return nil, err
	}
	v := legalView(out)
	return &v, nil
}

func (l *LegalService) UpdateDraft(ctx context.Context, p *auth.Principal, docID, title, body string) (*LegalDoc, error) {
	d, err := l.s.Q.UpdateLegalDraft(ctx, sqlcgen.UpdateLegalDraftParams{ID: docID, Title: title, Body: body})
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.Conflict(apperr.CodeInvalidState, "dokumen tidak ada atau sudah terbit (versi terbit tidak dapat diubah)")
		}
		return nil, err
	}
	v := legalView(d)
	return &v, nil
}

func (l *LegalService) Publish(ctx context.Context, p *auth.Principal, docID string) (*LegalDoc, error) {
	var out sqlcgen.LegalDocument
	err := l.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		out, err = q.PublishLegalDocument(ctx, docID)
		if err != nil {
			if platform.IsNoRows(err) {
				return apperr.Conflict(apperr.CodeInvalidState, "dokumen tidak ada atau sudah terbit")
			}
			return err
		}
		return l.s.Audit.Log(ctx, q, p, "legal.publish", "legal_document", out.ID, nil, map[string]any{"kind": out.Kind, "version": out.Version})
	})
	if err != nil {
		return nil, err
	}
	v := legalView(out)
	return &v, nil
}

// ---- Cookie consent ----

type CookieConsent struct {
	Analytics bool `json:"analytics"`
	Marketing bool `json:"marketing"`
}

func (l *LegalService) GetCookie(ctx context.Context, subject string) (CookieConsent, error) {
	if subject == "" {
		return CookieConsent{}, nil
	}
	c, err := l.s.Q.GetCookieConsent(ctx, subject)
	if err != nil {
		if platform.IsNoRows(err) {
			return CookieConsent{}, nil // tanpa persetujuan = hanya cookie esensial
		}
		return CookieConsent{}, err
	}
	return CookieConsent{Analytics: c.Analytics, Marketing: c.Marketing}, nil
}

func (l *LegalService) PutCookie(ctx context.Context, subject, userID string, c CookieConsent) (CookieConsent, error) {
	if len(subject) < 8 || len(subject) > 64 {
		return c, apperr.Validation("subject cookie tidak valid")
	}
	_, err := l.s.Q.UpsertCookieConsent(ctx, sqlcgen.UpsertCookieConsentParams{SubjectKey: subject, UserID: strPtr(userID), Analytics: c.Analytics, Marketing: c.Marketing})
	return c, err
}
