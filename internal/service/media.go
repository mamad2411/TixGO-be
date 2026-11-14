package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/imgproc"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/storage"
)

// MediaService = unggah gambar + varian (M45), penyajian publik bercache panjang.
type MediaService struct{ s *Services }

func newMediaService(s *Services) *MediaService { return &MediaService{s: s} }

type MediaVariant struct {
	W   int    `json:"w"`
	H   int    `json:"h"`
	Key string `json:"key"`
}

type MediaView struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Width  int               `json:"width"`
	Height int               `json:"height"`
	URLs   map[string]string `json:"urls"`
}

// MediaURL membentuk URL relatif varian (disajikan lewat /api/v1/media/:id/:w.jpg).
func MediaURL(assetID string, w int) string {
	if assetID == "" {
		return ""
	}
	return fmt.Sprintf("/api/v1/media/%s/%d.jpg", assetID, w)
}

var mediaKindRoles = map[string][]string{
	"poster": {"organizer", "organizer_member", "admin", "editor"},
	"artist": {"organizer", "organizer_member", "admin", "editor"},
	"venue":  {"organizer", "organizer_member", "admin", "editor"},
	"banner": {"admin", "editor"},
	"photo":  {"customer", "organizer", "organizer_member", "gate_staff", "support", "finance", "editor", "admin"},
	"avatar": {"customer", "organizer", "organizer_member", "gate_staff", "support", "finance", "editor", "admin"},
}

func roleAllowed(role string, allowed []string) bool {
	for _, r := range allowed {
		if r == role {
			return true
		}
	}
	return false
}

func (m *MediaService) Upload(ctx context.Context, p *auth.Principal, kind string, r io.Reader) (*MediaView, error) {
	roles, ok := mediaKindRoles[kind]
	if !ok {
		return nil, apperr.Validation("kind harus poster, artist, venue, banner, avatar, atau photo").With("field", "kind")
	}
	if !roleAllowed(p.Role, roles) {
		return nil, apperr.Forbidden("peran tidak boleh mengunggah " + kind)
	}
	if okRL, _, err := m.s.Limiter.Allow(ctx, "upload:"+p.UserID, 30, 10*60*1e9); err == nil && !okRL {
		return nil, apperr.TooMany("terlalu banyak unggahan")
	}
	res, err := imgproc.Process(r)
	if err != nil {
		switch err {
		case imgproc.ErrTooLarge:
			return nil, apperr.Validation("gambar maksimal 8 MB")
		case imgproc.ErrHugeDecoded:
			return nil, apperr.Validation("dimensi gambar terlalu besar")
		default:
			return nil, apperr.Validation("berkas bukan gambar JPEG/PNG/WebP yang valid")
		}
	}
	assetID := id.New()
	vars := make([]MediaVariant, 0, len(res.Variants))
	uploaded := []string{}
	for _, v := range res.Variants {
		key := fmt.Sprintf("media/%s/%d.jpg", assetID, v.Width)
		if err := m.s.Store.Put(ctx, key, bytes.NewReader(v.Data), int64(len(v.Data)), "image/jpeg"); err != nil {
			for _, k := range uploaded {
				_ = m.s.Store.Delete(ctx, k)
			}
			return nil, err
		}
		uploaded = append(uploaded, key)
		vars = append(vars, MediaVariant{W: v.Width, H: v.Height, Key: key})
	}
	vj, _ := json.Marshal(vars)
	w, h := res.Width, res.Height
	_, err = m.s.Q.InsertMedia(ctx, sqlcgen.InsertMediaParams{ID: assetID, OwnerUserID: strPtr(p.UserID), Kind: kind,
		ObjectKey: uploaded[len(uploaded)-1], ContentType: "image/jpeg", Width: &w, Height: &h, Variants: vj})
	if err != nil {
		for _, k := range uploaded {
			_ = m.s.Store.Delete(ctx, k)
		}
		return nil, err
	}
	urls := map[string]string{}
	for _, v := range vars {
		urls[fmt.Sprint(v.W)] = MediaURL(assetID, v.W)
	}
	return &MediaView{ID: assetID, Kind: kind, Width: res.Width, Height: res.Height, URLs: urls}, nil
}

// Owned memastikan aset milik pengguna dan berjenis tertentu (dipakai poster_key/avatar).
func (m *MediaService) Owned(ctx context.Context, userID, assetID, kind string) error {
	a, err := m.s.Q.GetMedia(ctx, assetID)
	if err != nil {
		if platform.IsNoRows(err) {
			return apperr.Validation("aset gambar tidak ditemukan").With("field", "poster_asset_id")
		}
		return err
	}
	if a.Kind != kind || deref(a.OwnerUserID) != userID {
		return apperr.Forbidden("aset gambar bukan milik Anda")
	}
	return nil
}

// Open menyajikan varian terdekat ≥ width (atau yang terbesar). Hanya jenis publik.
func (m *MediaService) Open(ctx context.Context, assetID string, width int) (io.ReadCloser, storage.Info, error) {
	a, err := m.s.Q.GetMedia(ctx, assetID)
	if err != nil {
		return nil, storage.Info{}, apperr.NotFound("gambar")
	}
	switch a.Kind {
	case "other":
		return nil, storage.Info{}, apperr.NotFound("gambar")
	case "photo": // foto galeri hanya disajikan setelah lolos moderasi
		if ok, _ := m.s.Q.PhotoIsPublished(ctx, assetID); !ok {
			return nil, storage.Info{}, apperr.NotFound("gambar")
		}
	}
	// Hanya lebar standar yang boleh diminta; lebar lain 404 (mencegah pemindaian ukuran sembarang).
	if !slices.Contains(imgproc.Widths, width) {
		return nil, storage.Info{}, apperr.NotFound("gambar")
	}
	var vars []MediaVariant
	_ = json.Unmarshal(a.Variants, &vars)
	// Varian terkecil yang ≥ width; bila tidak ada (gambar asli lebih kecil dari yang diminta, tidak di-upscale),
	// pakai yang terbesar. Tanpa ini URL 800/1600 mengembalikan 404 untuk gambar kecil.
	var pick *MediaVariant
	for i := range vars {
		v := &vars[i]
		switch {
		case pick == nil:
			pick = v
		case pick.W < width && v.W > pick.W: // masih di bawah target: ambil yang lebih besar
			pick = v
		case v.W >= width && v.W < pick.W: // sudah ≥ target: ambil yang paling dekat
			pick = v
		}
	}
	if pick == nil {
		return nil, storage.Info{}, apperr.NotFound("gambar")
	}
	rc, info, err := m.s.Store.Get(ctx, pick.Key)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, storage.Info{}, apperr.NotFound("gambar")
		}
		return nil, storage.Info{}, err
	}
	return rc, info, nil
}
