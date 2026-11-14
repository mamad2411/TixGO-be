package apitest

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"time"

	"tixgo/backend/internal/auth"
)

// Upload mengirim multipart (field file + field teks).
func (c *Client) Upload(path string, fields map[string]string, fileField, filename string, data []byte) *Resp {
	c.e.T.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile(fileField, filename)
	if err != nil {
		c.e.T.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = c.IP + ":1234"
	req.Header.Set("User-Agent", c.UA)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	w := httptest.NewRecorder()
	c.e.Engine.ServeHTTP(w, req)
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	return &Resp{Code: res.StatusCode, Body: b, Header: res.Header, t: c.e.T}
}

func pngBytes(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), B: 90, A: 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// EnableMFA mengaktifkan MFA langsung di DB untuk pengguna lalu login ulang dengan kode TOTP → klien ber-klaim mfa.
func (e *Env) EnableMFA(c *Client) *Client {
	e.T.Helper()
	secret := auth.NewTOTPSecret()
	enc, _ := e.Svc.Box.Encrypt([]byte(secret))
	e.Exec("INSERT INTO mfa_totp (user_id, secret_enc, confirmed_at) VALUES ($1,$2,now()) ON CONFLICT (user_id) DO UPDATE SET secret_enc=$2, confirmed_at=now()", c.UserID, enc)
	e.Exec("UPDATE users SET mfa_enabled=true WHERE id=$1", c.UserID)
	code, _ := auth.TOTPCode(secret, e.Clock.Now(), 30)
	nc := e.Anon()
	var d struct {
		AccessToken string `json:"access_token"`
	}
	nc.POST(api+"/auth/login", M{"email": c.Email, "password": testPassword, "mfa_code": code}).Expect(200).Data(&d)
	nc.Token, nc.Email, nc.UserID = d.AccessToken, c.Email, c.UserID
	return nc
}

// Org = organizer uji: pemilik (ber-MFA), id organizer.
type Org struct {
	C  *Client
	ID string
}

// NewOrganizer mendaftarkan organizer ber-MFA (belum KYB). approve=true → langsung disetujui lewat SQL (jalan pintas).
func (e *Env) NewOrganizer(approve bool) *Org {
	e.T.Helper()
	o := e.NewOrganizerNoMFA(approve)
	o.C = e.EnableMFA(o.C)
	return o
}

// NewOrganizerNoMFA = pemilik organizer yang BELUM mengaktifkan MFA (untuk menguji penolakan aksi sensitif).
func (e *Env) NewOrganizerNoMFA(approve bool) *Org {
	e.T.Helper()
	c := e.Register(uniqEmail("org"), "Penyelenggara")
	e.Exec("UPDATE users SET email_verified_at=now() WHERE id=$1", c.UserID)
	var o struct {
		ID string `json:"id"`
	}
	c.POST(api+"/organizer/apply", M{"name": "Panggung Kita", "legal_name": "PT Panggung Kita", "contact_phone": "+6281100000000", "address": "Jl. Merdeka 1, Jakarta"}).Expect(201).Data(&o)
	c = e.Login(c.Email) // klaim peran 'organizer'
	if approve {
		e.Exec("UPDATE organizers SET kyb_status='approved' WHERE id=$1", o.ID)
	}
	return &Org{C: c, ID: o.ID}
}

// NewVenue membuat venue (zona Asia/Jakarta).
func (e *Env) NewVenue(c *Client, city string) string {
	e.T.Helper()
	var v struct {
		ID string `json:"id"`
	}
	c.POST(api+"/organizer/venues", M{"name": "Istora " + city, "city": city, "tz": "Asia/Jakarta", "capacity": 10000}).Expect(201).Data(&v)
	return v.ID
}

func (e *Env) future(days int) string {
	return e.Clock.Now().Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
}

// EventSpec = parameter pembuatan event uji.
type EventSpec struct {
	Title   string
	City    string
	Genre   string
	Mode    string // general|seated
	Price   int64
	Quota   int
	Queue   bool
	Extra   M
	NoPhase bool
}

// Ev = event uji yang sudah diterbitkan.
type Ev struct {
	ID, Slug, TierID, VenueID string
	Org                       *Org
}

// PublishedEvent membuat event hingga status published lewat API (submit → admin approve → publish).
func (e *Env) PublishedEvent(o *Org, sp EventSpec) *Ev {
	e.T.Helper()
	if sp.Title == "" {
		sp.Title = "Konser Uji"
	}
	if sp.City == "" {
		sp.City = "Jakarta"
	}
	if sp.Mode == "" {
		sp.Mode = "general"
	}
	if sp.Quota == 0 {
		sp.Quota = 100
	}
	venue := e.NewVenue(o.C, sp.City)
	body := M{"venue_id": venue, "title": sp.Title, "genre": sp.Genre, "mode": sp.Mode, "starts_at": e.future(30), "ends_at": e.future(31),
		"queue_required": sp.Queue, "max_per_account": 6, "max_per_order": 6}
	for k, v := range sp.Extra {
		body[k] = v
	}
	var ev struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	o.C.POST(api+"/organizer/events", body).Expect(201).Data(&ev)
	var tier struct {
		ID string `json:"id"`
	}
	tb := M{"name": "Reguler", "price": sp.Price}
	if sp.Mode == "general" {
		tb["quota"] = sp.Quota
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/tiers", tb).Expect(201).Data(&tier)
	if sp.Mode == "seated" {
		o.C.POST(api+"/organizer/events/"+ev.ID+"/seatmap/generate", M{"sections": []M{{"name": "A", "tier_id": tier.ID, "rows": 5, "seats_per_row": sp.Quota / 5}}}).Expect(200)
	}
	if !sp.NoPhase {
		o.C.POST(api+"/organizer/events/"+ev.ID+"/phases", M{"name": "Umum", "audience": "public", "starts_at": e.Clock.Now().Add(-time.Hour).Format(time.RFC3339), "ends_at": e.future(29)}).Expect(201)
	}
	o.C.POST(api+"/organizer/events/"+ev.ID+"/submit", nil).Expect(200)
	admin := e.UserWithRole("admin", "Admin Review")
	admin.POST(api+"/admin/events/"+ev.ID+"/review", M{"decision": "approve"}).Expect(200)
	o.C.POST(api+"/organizer/events/"+ev.ID+"/publish", nil).Expect(200)
	return &Ev{ID: ev.ID, Slug: ev.Slug, TierID: tier.ID, VenueID: venue, Org: o}
}

var _ = fmt.Sprint
var _ http.Header
