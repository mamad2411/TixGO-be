package service

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// CMSService = halaman, artikel, bantuan, banner, koleksi kurasi (docs/20 M40). Alur: draft → in_review → published (versi tersimpan).
type CMSService struct {
	s   *Services
	md  goldmark.Markdown
	pol *bluemonday.Policy
}

func newCMSService(s *Services) *CMSService {
	return &CMSService{s: s, md: goldmark.New(), pol: bluemonday.UGCPolicy()}
}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type PageInput struct {
	Kind     string         `json:"kind" binding:"required,oneof=page article help collection"`
	Slug     string         `json:"slug" binding:"required,min=2,max=80"`
	Locale   string         `json:"locale" binding:"required,oneof=id en"`
	Title    string         `json:"title" binding:"required,min=2,max=160"`
	Body     string         `json:"body" binding:"required,max=100000"` // Markdown
	Category string         `json:"category" binding:"omitempty,max=60"`
	SEO      map[string]any `json:"seo"`
}

type PageView struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Slug      string          `json:"slug"`
	Locale    string          `json:"locale"`
	Title     string          `json:"title"`
	Body      string          `json:"body,omitempty"`
	BodyHTML  string          `json:"body_html"`
	Category  *string         `json:"category,omitempty"`
	SEO       json.RawMessage `json:"seo"`
	Status    string          `json:"status"`
	Version   int             `json:"version"`
	PublishAt *time.Time      `json:"publish_at,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// render: Markdown → HTML → disanitasi (tidak ada skrip/handler inline; AC3).
func (c *CMSService) render(src string) string {
	var buf bytes.Buffer
	if err := c.md.Convert([]byte(src), &buf); err != nil {
		return ""
	}
	return c.pol.Sanitize(buf.String())
}

func (c *CMSService) view(p sqlcgen.CmsPage, withSource bool) PageView {
	v := PageView{ID: p.ID, Kind: p.Kind, Slug: p.Slug, Locale: p.Locale, Title: p.Title, BodyHTML: c.render(p.Body), Category: p.Category, SEO: p.Seo,
		Status: p.Status, Version: p.Version, PublishAt: p.PublishAt, UpdatedAt: p.UpdatedAt}
	if withSource {
		v.Body = p.Body
	}
	return v
}

func (c *CMSService) seoJSON(m map[string]any) ([]byte, error) {
	clean := map[string]any{}
	for _, k := range []string{"title", "description", "og_image"} {
		if s, ok := m[k].(string); ok {
			if len([]rune(s)) > 300 {
				return nil, apperr.Validation("seo." + k + " maksimal 300 karakter")
			}
			clean[k] = s
		}
	}
	return json.Marshal(clean)
}

// Create membuat draf versi pertama; bila slug sudah ada, membuat versi baru berikutnya (draf).
func (c *CMSService) Create(ctx context.Context, p *auth.Principal, in PageInput) (*PageView, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !slugRe.MatchString(in.Slug) {
		return nil, apperr.Validation("slug: huruf kecil, angka, dan '-'").With("field", "slug")
	}
	seo, err := c.seoJSON(in.SEO)
	if err != nil {
		return nil, err
	}
	var out sqlcgen.CmsPage
	err = c.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		ver := 1
		if last, err := q.LatestCMSVersion(ctx, sqlcgen.LatestCMSVersionParams{Kind: in.Kind, Slug: in.Slug, Locale: in.Locale}); err == nil {
			if last.Status == "draft" || last.Status == "in_review" {
				return apperr.Conflict(apperr.CodeConflict, "sudah ada versi draf untuk halaman ini; sunting versi tersebut")
			}
			ver = last.Version + 1
		} else if !platform.IsNoRows(err) {
			return err
		}
		out, err = q.InsertCMSPage(ctx, sqlcgen.InsertCMSPageParams{ID: id.New(), Kind: in.Kind, Slug: in.Slug, Locale: in.Locale, Title: in.Title, Body: in.Body,
			Category: strPtr(in.Category), Seo: seo, Version: ver, UpdatedBy: &p.UserID})
		if err != nil {
			return err
		}
		return c.s.Audit.Log(ctx, q, p, "cms.create", "cms_page", out.ID, nil, map[string]any{"slug": in.Slug, "version": ver})
	})
	if err != nil {
		return nil, err
	}
	v := c.view(out, true)
	return &v, nil
}

func (c *CMSService) Update(ctx context.Context, p *auth.Principal, pageID string, in PageInput) (*PageView, error) {
	seo, err := c.seoJSON(in.SEO)
	if err != nil {
		return nil, err
	}
	var out sqlcgen.CmsPage
	err = c.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		cur, err := q.GetCMSPageForUpdate(ctx, pageID)
		if err != nil {
			return apperr.NotFound("halaman")
		}
		if cur.Status != "draft" {
			return apperr.Conflict(apperr.CodeInvalidState, "hanya draf yang dapat disunting; buat versi baru untuk mengubah halaman terbit")
		}
		out, err = q.UpdateCMSDraft(ctx, sqlcgen.UpdateCMSDraftParams{ID: pageID, Title: in.Title, Body: in.Body, Category: strPtr(in.Category), Seo: seo, UpdatedBy: &p.UserID})
		return err
	})
	if err != nil {
		return nil, err
	}
	v := c.view(out, true)
	return &v, nil
}

// SubmitReview: draft → in_review (penulis). Publish: in_review → published (admin/editor lain; penulis tak boleh menerbitkan sendiri).
func (c *CMSService) SubmitReview(ctx context.Context, p *auth.Principal, pageID string) (*PageView, error) {
	return c.transition(ctx, p, pageID, "draft", "in_review", nil, "cms.submit")
}

func (c *CMSService) Publish(ctx context.Context, p *auth.Principal, pageID string, at *time.Time) (*PageView, error) {
	if at != nil && !at.After(c.s.now()) {
		return nil, apperr.Validation("publish_at harus di masa depan").With("field", "publish_at")
	}
	return c.transition(ctx, p, pageID, "in_review", "published", at, "cms.publish")
}

func (c *CMSService) Reject(ctx context.Context, p *auth.Principal, pageID string) (*PageView, error) {
	return c.transition(ctx, p, pageID, "in_review", "draft", nil, "cms.reject")
}

func (c *CMSService) transition(ctx context.Context, p *auth.Principal, pageID, from, to string, at *time.Time, action string) (*PageView, error) {
	var out sqlcgen.CmsPage
	err := c.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		cur, err := q.GetCMSPageForUpdate(ctx, pageID)
		if err != nil {
			return apperr.NotFound("halaman")
		}
		if cur.Status != from {
			return apperr.Conflict(apperr.CodeInvalidState, "status halaman harus "+from)
		}
		if to == "published" && cur.UpdatedBy != nil && *cur.UpdatedBy == p.UserID && p.Role != "admin" {
			return apperr.Forbidden("penerbit harus orang lain dari penyunting terakhir (kecuali admin)")
		}
		if to == "published" && at != nil { // terjadwal: tetap in_review sampai waktunya
			out, err = q.SetCMSStatus(ctx, sqlcgen.SetCMSStatusParams{ID: pageID, Status: "in_review", PublishAt: at, UpdatedBy: &p.UserID})
			if err != nil {
				return err
			}
			return c.s.Audit.Log(ctx, q, p, "cms.schedule", "cms_page", pageID, nil, map[string]any{"at": at})
		}
		out, err = q.SetCMSStatus(ctx, sqlcgen.SetCMSStatusParams{ID: pageID, Status: to, UpdatedBy: &p.UserID})
		if err != nil {
			return err
		}
		if to == "published" {
			if _, err := q.ArchiveOtherPublished(ctx, sqlcgen.ArchiveOtherPublishedParams{Kind: cur.Kind, Slug: cur.Slug, Locale: cur.Locale, ID: cur.ID}); err != nil {
				return err
			}
		}
		return c.s.Audit.Log(ctx, q, p, action, "cms_page", pageID, map[string]any{"status": from}, map[string]any{"status": to})
	})
	if err != nil {
		return nil, err
	}
	v := c.view(out, true)
	return &v, nil
}

// RunScheduled (scheduler): terbitkan halaman terjadwal yang jatuh tempo.
func (c *CMSService) RunScheduled(ctx context.Context) (int, error) {
	due, err := c.s.Q.DueScheduledCMS(ctx, ptr(c.s.now()))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		err := c.s.InTx(ctx, func(q *sqlcgen.Queries) error {
			if _, err := q.SetCMSStatus(ctx, sqlcgen.SetCMSStatusParams{ID: d.ID, Status: "published", UpdatedBy: d.UpdatedBy}); err != nil {
				return err
			}
			_, err := q.ArchiveOtherPublished(ctx, sqlcgen.ArchiveOtherPublishedParams{Kind: d.Kind, Slug: d.Slug, Locale: d.Locale, ID: d.ID})
			return err
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (c *CMSService) List(ctx context.Context, kind, status, q string, limit, offset int) ([]PageView, error) {
	rows, err := c.s.Q.ListCMSPages(ctx, sqlcgen.ListCMSPagesParams{Kind: strPtr(kind), Status: strPtr(status), Q: strPtr(q), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]PageView, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.view(r, false))
	}
	return out, nil
}

func (c *CMSService) Get(ctx context.Context, pageID string) (*PageView, error) {
	p, err := c.s.Q.GetCMSPage(ctx, pageID)
	if err != nil {
		return nil, apperr.NotFound("halaman")
	}
	v := c.view(p, true)
	return &v, nil
}

func (c *CMSService) Versions(ctx context.Context, kind, slug, locale string) ([]sqlcgen.CMSVersionsRow, error) {
	return c.s.Q.CMSVersions(ctx, sqlcgen.CMSVersionsParams{Kind: kind, Slug: slug, Locale: locale})
}

// Public: halaman terbit; fallback ke locale lain TIDAK dilakukan diam-diam → 404 bila belum diterjemahkan.
func (c *CMSService) Public(ctx context.Context, kind, slug, locale string) (*PageView, error) {
	p, err := c.s.Q.PublishedCMSPage(ctx, sqlcgen.PublishedCMSPageParams{Kind: kind, Slug: slug, Locale: locale})
	if err != nil {
		return nil, apperr.NotFound("halaman")
	}
	v := c.view(p, false)
	return &v, nil
}

func (c *CMSService) PublicList(ctx context.Context, kind, locale, category string) ([]map[string]any, error) {
	rows, err := c.s.Q.ListPublishedCMS(ctx, sqlcgen.ListPublishedCMSParams{Kind: kind, Locale: locale, Category: strPtr(category)})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"slug": r.Slug, "title": r.Title, "category": r.Category, "updated_at": r.UpdatedAt})
	}
	return out, nil
}

func (c *CMSService) SearchHelp(ctx context.Context, locale, q string) ([]map[string]any, error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) < 2 {
		return []map[string]any{}, nil
	}
	rows, err := c.s.Q.SearchHelp(ctx, sqlcgen.SearchHelpParams{Locale: locale, Q: q})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"slug": r.Slug, "title": r.Title, "category": r.Category, "snippet": c.pol.Sanitize(r.Snippet)})
	}
	return out, nil
}

// ---------- banner & koleksi ----------

type BannerInput struct {
	Title    string     `json:"title" binding:"required,min=2,max=120"`
	ImageKey string     `json:"image_key" binding:"required,len=26"` // id media_asset kind=banner
	Link     string     `json:"link" binding:"omitempty,max=300"`
	StartsAt time.Time  `json:"starts_at" binding:"required"`
	EndsAt   *time.Time `json:"ends_at"`
	Sort     int        `json:"sort"`
}

func (c *CMSService) CreateBanner(ctx context.Context, p *auth.Principal, in BannerInput) (*sqlcgen.Banner, error) {
	if in.Link != "" && !strings.HasPrefix(in.Link, "/") && !strings.HasPrefix(in.Link, "https://") {
		return nil, apperr.Validation("link harus path internal (/..) atau https").With("field", "link")
	}
	if in.EndsAt != nil && !in.EndsAt.After(in.StartsAt) {
		return nil, apperr.Validation("ends_at harus setelah starts_at").With("field", "ends_at")
	}
	if err := c.s.Media.Owned(ctx, p.UserID, in.ImageKey, "banner"); err != nil {
		return nil, err
	}
	b, err := c.s.Q.InsertBanner(ctx, sqlcgen.InsertBannerParams{ID: id.New(), Title: in.Title, ImageKey: in.ImageKey, Link: strPtr(in.Link), StartsAt: in.StartsAt, EndsAt: in.EndsAt, Sort: in.Sort})
	return &b, err
}

func (c *CMSService) Banners(ctx context.Context, activeOnly bool) ([]map[string]any, error) {
	var rows []sqlcgen.Banner
	var err error
	if activeOnly {
		rows, err = c.s.Q.ActiveBanners(ctx, c.s.now())
	} else {
		rows, err = c.s.Q.ListBanners(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, map[string]any{"id": b.ID, "title": b.Title, "image_url": MediaURL(b.ImageKey, 1600), "link": b.Link, "starts_at": b.StartsAt, "ends_at": b.EndsAt, "sort": b.Sort})
	}
	return out, nil
}

func (c *CMSService) DeleteBanner(ctx context.Context, bannerID string) error {
	n, err := c.s.Q.DeleteBanner(ctx, bannerID)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("banner")
	}
	return nil
}

type CollectionInput struct {
	Slug     string     `json:"slug" binding:"required,min=2,max=60"`
	Title    string     `json:"title" binding:"required,min=2,max=120"`
	EventIDs []string   `json:"event_ids" binding:"required,max=60,dive,len=26"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

func (c *CMSService) SaveCollection(ctx context.Context, p *auth.Principal, in CollectionInput) (*sqlcgen.Collection, error) {
	if !slugRe.MatchString(in.Slug) {
		return nil, apperr.Validation("slug tidak valid").With("field", "slug")
	}
	col, err := c.s.Q.UpsertCollection(ctx, sqlcgen.UpsertCollectionParams{ID: id.New(), Slug: in.Slug, Title: in.Title, EventIds: in.EventIDs, StartsAt: in.StartsAt, EndsAt: in.EndsAt})
	if err != nil {
		return nil, err
	}
	_ = c.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		return c.s.Audit.Log(ctx, q, p, "cms.collection", "collection", col.ID, nil, map[string]any{"slug": in.Slug, "events": len(in.EventIDs)})
	})
	return &col, nil
}

// Collection (publik): hanya event yang sedang terbit & publik, urutan sesuai kurasi; di luar jendela waktu → 404.
func (c *CMSService) Collection(ctx context.Context, slug string) (map[string]any, error) {
	col, err := c.s.Q.GetCollectionBySlug(ctx, slug)
	if err != nil {
		return nil, apperr.NotFound("koleksi")
	}
	now := c.s.now()
	if (col.StartsAt != nil && now.Before(*col.StartsAt)) || (col.EndsAt != nil && !now.Before(*col.EndsAt)) {
		return nil, apperr.NotFound("koleksi")
	}
	cards, err := c.s.Catalog.CardsByIDs(ctx, col.EventIds)
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, e := range col.EventIds {
		order[e] = i
	}
	out := make([]EventCard, len(col.EventIds))
	n := 0
	for _, cd := range cards {
		if i, ok := order[cd.ID]; ok && i < len(out) {
			out[i] = cd
			n++
		}
	}
	list := make([]EventCard, 0, n)
	for _, cd := range out {
		if cd.ID != "" {
			list = append(list, cd)
		}
	}
	return map[string]any{"slug": col.Slug, "title": col.Title, "events": list}, nil
}

func (c *CMSService) Collections(ctx context.Context) ([]sqlcgen.Collection, error) {
	return c.s.Q.ListCollections(ctx)
}

func (c *CMSService) DeleteCollection(ctx context.Context, cid string) error {
	n, err := c.s.Q.DeleteCollection(ctx, cid)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("koleksi")
	}
	return nil
}
