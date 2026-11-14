package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// ReviewService = ulasan & galeri pasca-event (docs/20 M41, P2).
type ReviewService struct{ s *Services }

func newReviewService(s *Services) *ReviewService { return &ReviewService{s: s} }

const (
	minReviewsForAggregate = 5 // rating agregat tidak ditampilkan di bawah ini (AC4, T)
	reportsToHide          = 3 // laporan sebanyak ini menyembunyikan ulasan ke antrean moderasi
)

// kata terlarang awal (T): daftar kecil dapat diganti lewat config di iterasi berikutnya; cocok kata utuh, tidak peka huruf.
var bannedWords = regexp.MustCompile(`(?i)\b(anjing|bangsat|bajingan|tolol|goblok|kontol|memek|ngentot|babi)\b`)
var urlRe = regexp.MustCompile(`(?i)(https?://|www\.)`)

type ReviewInput struct {
	Rating int    `json:"rating" binding:"required,min=1,max=5"`
	Body   string `json:"body" binding:"omitempty,max=2000"`
}

type ReviewView struct {
	ID        string    `json:"id"`
	Rating    int       `json:"rating"`
	Body      *string   `json:"body,omitempty"`
	Reply     *string   `json:"organizer_reply,omitempty"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

func firstName(n string) string {
	if f := strings.Fields(n); len(f) > 0 {
		return f[0] // nama depan saja (minim PII)
	}
	return "Penonton"
}

// Submit: hanya pemilik tiket `used`; satu ulasan per tiket. Lolos filter → terbit; tidak → antrean moderasi manual.
func (r *ReviewService) Submit(ctx context.Context, userID, ticketID string, in ReviewInput) (*sqlcgen.Review, error) {
	tk, err := r.s.Ticket.ownTicket(ctx, userID, ticketID)
	if err != nil {
		return nil, err
	}
	if tk.Status != "used" {
		return nil, apperr.Unprocessable(apperr.CodeValidation, "ulasan hanya untuk tiket yang sudah dipakai masuk")
	}
	body := strings.TrimSpace(in.Body)
	status := "published"
	if body != "" && (bannedWords.MatchString(body) || urlRe.MatchString(body)) {
		status = "pending"
	}
	rv, err := r.s.Q.InsertReview(ctx, sqlcgen.InsertReviewParams{ID: id.New(), TicketID: tk.ID, EventID: tk.EventID, UserID: userID, Rating: in.Rating, Body: strPtr(body), Status: status})
	if err != nil {
		if platform.IsUniqueViolation(err) {
			return nil, apperr.Conflict(apperr.CodeConflict, "tiket ini sudah diulas")
		}
		return nil, err
	}
	return &rv, nil
}

type EventReviews struct {
	Count   int          `json:"count"`
	Average *float64     `json:"average,omitempty"` // hanya bila count ≥ minReviewsForAggregate
	Reviews []ReviewView `json:"reviews"`
}

func (r *ReviewService) ForEvent(ctx context.Context, eventKey string, limit, offset int) (*EventReviews, error) {
	ev, err := r.s.Catalog.lookup(ctx, eventKey)
	if err != nil || ev.Visibility != "public" || (ev.Status != stPublished && ev.Status != stFinished && ev.Status != stPaused) {
		return nil, apperr.NotFound("event")
	}
	agg, err := r.s.Q.EventRatingAgg(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.Q.ListEventReviews(ctx, sqlcgen.ListEventReviewsParams{EventID: ev.ID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := &EventReviews{Count: int(agg.N), Reviews: make([]ReviewView, 0, len(rows))}
	if int(agg.N) >= minReviewsForAggregate {
		a := agg.Avg
		out.Average = &a
	}
	for _, x := range rows {
		out.Reviews = append(out.Reviews, ReviewView{ID: x.ID, Rating: x.Rating, Body: x.Body, Reply: x.OrganizerReply, Author: firstName(x.Author), CreatedAt: x.CreatedAt})
	}
	return out, nil
}

// Report: pengguna melaporkan ulasan; ≥3 laporan menyembunyikan ke moderasi.
func (r *ReviewService) Report(ctx context.Context, userID, reviewID, reason string) error {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < 5 {
		return apperr.Validation("alasan laporan wajib diisi").With("field", "reason")
	}
	rv, err := r.s.Q.GetReview(ctx, reviewID)
	if err != nil || rv.Status != "published" {
		return apperr.NotFound("ulasan")
	}
	if rv.UserID == userID {
		return apperr.Conflict(apperr.CodeConflict, "tidak dapat melaporkan ulasan sendiri")
	}
	if _, err := r.s.Q.InsertReviewReport(ctx, sqlcgen.InsertReviewReportParams{ID: id.New(), ReviewID: reviewID, ReporterID: userID, Reason: reason}); err != nil {
		return err
	}
	if n, _ := r.s.Q.CountReviewReports(ctx, reviewID); int(n) >= reportsToHide {
		_, _ = r.s.Q.SetReviewStatus(ctx, sqlcgen.SetReviewStatusParams{ID: reviewID, Status: "pending"})
	}
	return nil
}

// Reply: hak balas organizer pemilik event (satu balasan, dapat diganti).
func (r *ReviewService) Reply(ctx context.Context, p *auth.Principal, sc OrgScope, eventID, reviewID, text string) error {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < 2 || len([]rune(text)) > 1000 {
		return apperr.Validation("balasan 2–1000 karakter").With("field", "reply")
	}
	if bannedWords.MatchString(text) {
		return apperr.Validation("balasan memuat kata yang tidak diperbolehkan").With("field", "reply")
	}
	return r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := r.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		n, err := q.SetReviewReply(ctx, sqlcgen.SetReviewReplyParams{ID: reviewID, EventID: eventID, OrganizerReply: &text})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("ulasan")
		}
		return r.s.Audit.Log(ctx, q, p, "review.reply", "review", reviewID, nil, nil)
	})
}

type ModerationItem struct {
	ID      string  `json:"id"`
	Rating  int     `json:"rating"`
	Body    *string `json:"body"`
	Status  string  `json:"status"`
	Reports int     `json:"reports"`
	EventID string  `json:"event_id"`
}

func (r *ReviewService) Queue(ctx context.Context, status string, limit, offset int) ([]ModerationItem, error) {
	rows, err := r.s.Q.ModerationReviews(ctx, sqlcgen.ModerationReviewsParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]ModerationItem, 0, len(rows))
	for _, x := range rows {
		out = append(out, ModerationItem{ID: x.ID, Rating: x.Rating, Body: x.Body, Status: x.Status, Reports: int(x.Reports), EventID: x.EventID})
	}
	return out, nil
}

func (r *ReviewService) Moderate(ctx context.Context, p *auth.Principal, reviewID, decision string) error {
	st := map[string]string{"approve": "published", "reject": "rejected"}[decision]
	if st == "" {
		return apperr.Validation("decision harus approve atau reject").With("field", "decision")
	}
	return r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetReviewStatus(ctx, sqlcgen.SetReviewStatusParams{ID: reviewID, Status: st})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("ulasan")
		}
		return r.s.Audit.Log(ctx, q, p, "review."+decision, "review", reviewID, nil, nil)
	})
}

// ---------- galeri ----------

type PhotoInput struct {
	AssetID         string `json:"asset_id" binding:"required,len=26"`
	RightsConfirmed bool   `json:"rights_confirmed"`
}

// AddPhoto: peserta terverifikasi (punya tiket used) atau anggota organizer; wajib menyatakan hak cipta/izin; masuk moderasi.
func (r *ReviewService) AddPhoto(ctx context.Context, userID, eventKey string, in PhotoInput) (*sqlcgen.EventPhoto, error) {
	if !in.RightsConfirmed {
		return nil, apperr.Validation("kamu harus menyatakan memiliki hak atas foto ini").With("field", "rights_confirmed")
	}
	ev, err := r.s.Catalog.lookup(ctx, eventKey)
	if err != nil {
		return nil, apperr.NotFound("event")
	}
	if _, err := r.s.Q.UserUsedTicketForEvent(ctx, sqlcgen.UserUsedTicketForEventParams{OwnerUserID: userID, EventID: ev.ID}); err != nil {
		sc, _, e2 := r.s.Organizer.Membership(ctx, userID)
		if e2 != nil || sc == nil || sc.OrganizerID != ev.OrganizerID {
			return nil, apperr.Forbidden("hanya peserta yang sudah hadir atau penyelenggara yang dapat mengunggah foto")
		}
	}
	if err := r.s.Media.Owned(ctx, userID, in.AssetID, "photo"); err != nil {
		return nil, err
	}
	ph, err := r.s.Q.InsertEventPhoto(ctx, sqlcgen.InsertEventPhotoParams{ID: id.New(), EventID: ev.ID, UserID: userID, ObjectKey: in.AssetID})
	return &ph, err
}

func (r *ReviewService) Photos(ctx context.Context, eventKey string) ([]map[string]any, error) {
	ev, err := r.s.Catalog.lookup(ctx, eventKey)
	if err != nil || ev.Visibility != "public" {
		return nil, apperr.NotFound("event")
	}
	rows, err := r.s.Q.ListEventPhotos(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		out = append(out, map[string]any{"id": x.ID, "url": MediaURL(x.ObjectKey, 1600), "thumb": MediaURL(x.ObjectKey, 400), "created_at": x.CreatedAt})
	}
	return out, nil
}

func (r *ReviewService) PhotoQueue(ctx context.Context, status string, limit, offset int) ([]sqlcgen.EventPhoto, error) {
	return r.s.Q.ModerationPhotos(ctx, sqlcgen.ModerationPhotosParams{Status: strPtr(status), Lim: int32(limit), Off: int32(offset)})
}

func (r *ReviewService) ModeratePhoto(ctx context.Context, p *auth.Principal, photoID, decision string) error {
	st := map[string]string{"approve": "published", "reject": "rejected"}[decision]
	if st == "" {
		return apperr.Validation("decision harus approve atau reject").With("field", "decision")
	}
	return r.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetPhotoStatus(ctx, sqlcgen.SetPhotoStatusParams{ID: photoID, Status: st})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("foto")
		}
		return r.s.Audit.Log(ctx, q, p, "photo."+decision, "photo", photoID, nil, nil)
	})
}
