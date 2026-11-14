package service

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// VenueService & ArtistService = master data venue/artis (docs/20 M3).
type VenueService struct{ s *Services }
type ArtistService struct{ s *Services }

func newVenueService(s *Services) *VenueService   { return &VenueService{s: s} }
func newArtistService(s *Services) *ArtistService { return &ArtistService{s: s} }

type VenueView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	City     string   `json:"city"`
	Address  *string  `json:"address"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
	TZ       string   `json:"tz"`
	Capacity *int     `json:"capacity"`
	ImageURL string   `json:"image_url,omitempty"`
	// Atribusi foto (wajib untuk lisensi CC BY/BY-SA) dan tautan sumbernya.
	ImageCredit string `json:"image_credit,omitempty"`
	ImageSource string `json:"image_source,omitempty"`
}

func venueView(v sqlcgen.Venue) VenueView {
	out := VenueView{ID: v.ID, Name: v.Name, City: v.City, Address: v.Address, Lat: v.Lat, Lng: v.Lng, TZ: v.Tz, Capacity: v.Capacity}
	if v.ImageKey != nil {
		out.ImageURL = MediaURL(*v.ImageKey, 1600)
		out.ImageCredit, out.ImageSource = deref(v.ImageCredit), deref(v.ImageSource)
	}
	return out
}

type VenueInput struct {
	Name     *string  `json:"name" binding:"omitempty,max=150"`
	City     *string  `json:"city" binding:"omitempty,max=80"`
	Address  *string  `json:"address" binding:"omitempty,max=300"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
	TZ       *string  `json:"tz" binding:"omitempty,max=64"`
	Capacity *int     `json:"capacity"`
	// ImageAssetID = id aset hasil POST /media (kind=venue).
	ImageAssetID *string `json:"image_asset_id" binding:"omitempty,len=26"`
	ImageCredit  *string `json:"image_credit" binding:"omitempty,max=200"`
}

func validTZ(tz string) bool {
	_, err := time.LoadLocation(tz)
	return err == nil && tz != "" && tz != "Local"
}

func (v *VenueService) validate(in VenueInput, requireAll bool) error {
	if requireAll && (in.Name == nil || in.City == nil) {
		return apperr.Validation("name dan city wajib")
	}
	if in.Name != nil {
		if l := len([]rune(strings.TrimSpace(*in.Name))); l < 2 || l > 150 {
			return apperr.Validation("nama venue 2–150 karakter").With("field", "name")
		}
	}
	if in.City != nil {
		if l := len([]rune(strings.TrimSpace(*in.City))); l < 2 || l > 80 {
			return apperr.Validation("kota 2–80 karakter").With("field", "city")
		}
	}
	if in.TZ != nil && !validTZ(*in.TZ) {
		return apperr.Validation("zona waktu IANA tidak valid (mis. Asia/Jakarta)").With("field", "tz")
	}
	if (in.Lat != nil) != (in.Lng != nil) {
		return apperr.Validation("lat dan lng harus diisi bersamaan")
	}
	if in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90 || *in.Lng < -180 || *in.Lng > 180) {
		return apperr.Validation("koordinat di luar rentang").With("field", "lat")
	}
	if in.Capacity != nil && (*in.Capacity < 1 || *in.Capacity > 500000) {
		return apperr.Validation("kapasitas 1–500000").With("field", "capacity")
	}
	return nil
}

// checkImage memastikan aset foto milik pemanggil dan berjenis venue.
func (v *VenueService) checkImage(ctx context.Context, p *auth.Principal, assetID *string) error {
	if assetID == nil {
		return nil
	}
	return v.s.Media.Owned(ctx, p.UserID, *assetID, "venue")
}

func (v *VenueService) Create(ctx context.Context, p *auth.Principal, in VenueInput) (*VenueView, error) {
	if err := v.validate(in, true); err != nil {
		return nil, err
	}
	tz := "Asia/Jakarta"
	if in.TZ != nil {
		tz = *in.TZ
	}
	if err := v.checkImage(ctx, p, in.ImageAssetID); err != nil {
		return nil, err
	}
	row, err := v.s.Q.InsertVenue(ctx, sqlcgen.InsertVenueParams{ID: id.New(), Name: strings.TrimSpace(*in.Name), City: strings.TrimSpace(*in.City),
		Address: in.Address, Lat: in.Lat, Lng: in.Lng, Tz: tz, Capacity: in.Capacity, CreatedBy: strPtr(p.UserID), ImageKey: in.ImageAssetID, ImageCredit: in.ImageCredit})
	if err != nil {
		return nil, err
	}
	out := venueView(row)
	return &out, nil
}

func (v *VenueService) Get(ctx context.Context, venueID string) (*VenueView, error) {
	row, err := v.s.Q.GetVenue(ctx, venueID)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("venue")
		}
		return nil, err
	}
	out := venueView(row)
	return &out, nil
}

func (v *VenueService) List(ctx context.Context, q, city string, limit, offset int) ([]VenueView, int, error) {
	rows, err := v.s.Q.ListVenues(ctx, sqlcgen.ListVenuesParams{Q: strPtr(q), City: strPtr(city), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := v.s.Q.CountVenues(ctx, sqlcgen.CountVenuesParams{Q: strPtr(q), City: strPtr(city)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]VenueView, 0, len(rows))
	for _, r := range rows {
		out = append(out, venueView(r))
	}
	return out, int(n), nil
}

// Update: pembuat venue atau admin. Venue yang sudah dipakai event terbit tidak boleh ganti zona waktu.
func (v *VenueService) Update(ctx context.Context, p *auth.Principal, venueID string, in VenueInput) (*VenueView, error) {
	if err := v.validate(in, false); err != nil {
		return nil, err
	}
	cur, err := v.s.Q.GetVenue(ctx, venueID)
	if err != nil {
		return nil, apperr.NotFound("venue")
	}
	if p.Role != "admin" && deref(cur.CreatedBy) != p.UserID {
		return nil, apperr.Forbidden("hanya pembuat venue atau admin")
	}
	if in.TZ != nil && *in.TZ != cur.Tz {
		var n int
		if err := v.s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE venue_id=$1 AND status IN ('published','paused')", venueID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, apperr.Conflict(apperr.CodeInvalidState, "zona waktu tidak dapat diubah: venue dipakai event yang sedang dijual")
		}
	}
	if err := v.checkImage(ctx, p, in.ImageAssetID); err != nil {
		return nil, err
	}
	row, err := v.s.Q.UpdateVenue(ctx, sqlcgen.UpdateVenueParams{ID: venueID, Name: in.Name, City: in.City, Address: in.Address, Lat: in.Lat, Lng: in.Lng, Tz: in.TZ, Capacity: in.Capacity, ImageKey: in.ImageAssetID, ImageCredit: in.ImageCredit})
	if err != nil {
		return nil, err
	}
	out := venueView(row)
	return &out, nil
}

// ---------- Artis ----------

type ArtistView struct {
	ID       string         `json:"id"`
	Slug     string         `json:"slug"`
	Name     string         `json:"name"`
	Bio      *string        `json:"bio"`
	ImageURL string         `json:"image_url,omitempty"`
	Socials  map[string]any `json:"socials"`
}

func artistView(a sqlcgen.Artist) ArtistView {
	v := ArtistView{ID: a.ID, Slug: a.Slug, Name: a.Name, Bio: a.Bio, Socials: map[string]any{}}
	_ = json.Unmarshal(a.Socials, &v.Socials)
	if a.ImageKey != nil {
		v.ImageURL = MediaURL(*a.ImageKey, 800)
	}
	return v
}

var slugBad = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify → huruf kecil, alfanumerik dipisah '-'; fallback "item".
func Slugify(s string) string {
	s = slugBad.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "item"
	}
	return s
}

func (a *ArtistService) Create(ctx context.Context, p *auth.Principal, name string, bio *string, imageAssetID *string, socials map[string]any) (*ArtistView, error) {
	name = strings.TrimSpace(name)
	if l := len([]rune(name)); l < 2 || l > 120 {
		return nil, apperr.Validation("nama artis 2–120 karakter").With("field", "name")
	}
	if imageAssetID != nil {
		if err := a.s.Media.Owned(ctx, p.UserID, *imageAssetID, "artist"); err != nil {
			return nil, err
		}
	}
	if socials == nil {
		socials = map[string]any{}
	}
	sj, _ := json.Marshal(socials)
	base := Slugify(name)
	for i := 0; i < 5; i++ {
		slug := base
		if i > 0 {
			slug = base + "-" + strings.ToLower(auth.RandomCode(4))
		}
		row, err := a.s.Q.InsertArtist(ctx, sqlcgen.InsertArtistParams{ID: id.New(), Slug: slug, Name: name, Bio: bio, ImageKey: imageAssetID, Socials: sj})
		if err == nil {
			v := artistView(row)
			return &v, nil
		}
		if !platform.IsUniqueViolation(err) {
			return nil, err
		}
	}
	return nil, apperr.Conflict(apperr.CodeConflict, "gagal membuat slug unik")
}

func (a *ArtistService) GetBySlug(ctx context.Context, slug string) (*ArtistView, error) {
	row, err := a.s.Q.GetArtistBySlug(ctx, slug)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("artis")
		}
		return nil, err
	}
	v := artistView(row)
	return &v, nil
}

func (a *ArtistService) List(ctx context.Context, q string, limit, offset int) ([]ArtistView, int, error) {
	rows, err := a.s.Q.ListArtists(ctx, sqlcgen.ListArtistsParams{Q: strPtr(q), Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	n, err := a.s.Q.CountArtists(ctx, strPtr(q))
	if err != nil {
		return nil, 0, err
	}
	out := make([]ArtistView, 0, len(rows))
	for _, r := range rows {
		out = append(out, artistView(r))
	}
	return out, int(n), nil
}

func (a *ArtistService) Update(ctx context.Context, p *auth.Principal, artistID string, name, bio, imageAssetID *string, socials map[string]any) (*ArtistView, error) {
	if p.Role != "admin" && p.Role != "editor" && p.Role != "organizer" && p.Role != "organizer_member" {
		return nil, apperr.Forbidden("tidak berwenang")
	}
	arg := sqlcgen.UpdateArtistParams{ID: artistID, Name: name, Bio: bio, ImageKey: imageAssetID}
	if name != nil {
		if l := len([]rune(strings.TrimSpace(*name))); l < 2 || l > 120 {
			return nil, apperr.Validation("nama artis 2–120 karakter").With("field", "name")
		}
	}
	if imageAssetID != nil {
		if err := a.s.Media.Owned(ctx, p.UserID, *imageAssetID, "artist"); err != nil {
			return nil, err
		}
	}
	if socials != nil {
		arg.Socials, _ = json.Marshal(socials)
	}
	row, err := a.s.Q.UpdateArtist(ctx, arg)
	if err != nil {
		if platform.IsNoRows(err) {
			return nil, apperr.NotFound("artis")
		}
		return nil, err
	}
	v := artistView(row)
	return &v, nil
}
