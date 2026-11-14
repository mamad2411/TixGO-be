// Package apidoc membangun OpenAPI 3 dari router Gin yang SEBENARNYA (route tidak mungkin tertinggal) +
// tipe Go untuk endpoint inti (skema permintaan/respons diturunkan dari struct, bukan ditulis tangan).
package apidoc

import (
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3gen"
	"github.com/gin-gonic/gin"

	"tixgo/backend/internal/service"
)

// Op = metadata endpoint. Req/Res boleh nil (skema generik).
type Op struct {
	Summary string
	Req     any
	Res     any
	List    bool // respons berupa daftar berpaginasi {data:[...], meta}
	Status  int  // default: 200 (POST pembuatan: 201)
}

// Docs: kunci "METHOD /api/v1/path/{param}".
var Docs = map[string]Op{
	"POST /api/v1/events/{slug}/holds":                     {Summary: "Tahan kursi/kuota (hold)", Req: service.HoldRequest{}, Res: service.HoldView{}, Status: 201},
	"GET /api/v1/holds/{id}":                               {Summary: "Detail hold", Res: service.HoldView{}},
	"POST /api/v1/holds/{id}/extend":                       {Summary: "Perpanjang hold sekali (Timer Tenang)", Res: service.HoldView{}},
	"POST /api/v1/events/{slug}/holds/auto":                {Summary: "Hold otomatis sesuai prioritas pilihan", Res: service.HoldView{}, Status: 201},
	"POST /api/v1/orders/quote":                            {Summary: "Rincian harga (harga jujur)", Req: service.QuoteReq{}, Res: service.Quote{}},
	"POST /api/v1/orders":                                  {Summary: "Buat order dari hold (wajib Idempotency-Key)", Req: service.CreateOrderReq{}, Res: service.OrderView{}, Status: 201},
	"GET /api/v1/orders/{id}":                              {Summary: "Detail order", Res: service.OrderView{}},
	"GET /api/v1/orders":                                   {Summary: "Daftar order saya", Res: service.OrderBrief{}, List: true},
	"POST /api/v1/orders/{id}/pay":                         {Summary: "Mulai pembayaran (Snap); idempoten per percobaan", Res: service.PayResult{}},
	"POST /api/v1/orders/{id}/cancel":                      {Summary: "Batalkan order pending"},
	"GET /api/v1/orders/{id}/invoice.pdf":                  {Summary: "Unduh invoice PDF"},
	"GET /api/v1/orders/{id}/refund-eligibility":           {Summary: "Pratinjau kelayakan & jumlah refund", Res: service.Eligibility{}},
	"POST /api/v1/orders/{id}/refunds":                     {Summary: "Ajukan refund (wajib Idempotency-Key)", Res: service.RefundView{}, Status: 201},
	"GET /api/v1/orders/{id}/refunds":                      {Summary: "Status refund order", Res: service.RefundView{}},
	"PUT /api/v1/refunds/{id}/account":                     {Summary: "Isi rekening refund (jalur manual)", Res: service.RefundView{}},
	"GET /api/v1/tickets":                                  {Summary: "Tiket saya", Res: service.TicketView{}, List: true},
	"GET /api/v1/tickets/{id}":                             {Summary: "Detail tiket", Res: service.TicketView{}},
	"GET /api/v1/tickets/{id}/qr":                          {Summary: "Payload QR berputar (TOTP)", Res: service.QRView{}},
	"GET /api/v1/tickets/{id}/plan":                        {Summary: "Rencana hari-H", Res: service.DayPlan{}},
	"POST /api/v1/tickets/{id}/transfer":                   {Summary: "Buat tautan klaim tiket", Res: service.ClaimLinkView{}, Status: 201},
	"POST /api/v1/claims/{token}/accept":                   {Summary: "Terima tiket kiriman", Res: service.TicketView{}},
	"POST /api/v1/gate/scan":                               {Summary: "Pindai QR (online, idempoten per scan_id)", Req: service.ScanReq{}, Res: service.ScanResult{}},
	"GET /api/v1/gate/events/{id}/manifest":                {Summary: "Paket data offline gate", Res: service.Manifest{}},
	"POST /api/v1/gate/events/{id}/sync":                   {Summary: "Sinkron antrean scan offline", Res: service.SyncResult{}},
	"GET /api/v1/gate/events/{id}/summary":                 {Summary: "Ringkasan check-in live", Res: service.GateSummary{}},
	"POST /api/v1/events/{slug}/waitlist":                  {Summary: "Gabung waitlist", Res: service.WaitlistView{}, Status: 201},
	"POST /api/v1/events/{slug}/squads":                    {Summary: "Buat squad", Req: service.SquadCreate{}, Res: service.SquadView{}, Status: 201},
	"GET /api/v1/squads/{id}":                              {Summary: "Detail squad", Res: service.SquadView{}},
	"POST /api/v1/squads/{id}/pay":                         {Summary: "Bayar bagianku (wajib Idempotency-Key)", Res: service.SquadPayResult{}, Status: 201},
	"POST /api/v1/tickets/{id}/resale":                     {Summary: "Pasang tiket untuk dijual ulang (plafon berlaku)", Res: service.ListingView{}, Status: 201},
	"POST /api/v1/resale/listings/{id}/buy":                {Summary: "Beli listing resale (wajib Idempotency-Key)", Res: service.OrderView{}, Status: 201},
	"GET /api/v1/passport":                                 {Summary: "Concert Passport saya", Res: service.PassportView{}},
	"POST /api/v1/concierge/ask":                           {Summary: "Tanya concierge (pencarian event; tanpa aksi)", Res: service.ConciergeAnswer{}},
	"GET /api/v1/readiness":                                {Summary: "Cek kesiapan war tiket", Res: service.Readiness{}},
	"GET /api/v1/clock":                                    {Summary: "Jam server", Res: service.ClockView{}},
	"GET /api/v1/status":                                   {Summary: "Status layanan", Res: service.ServiceStatus{}},
	"GET /api/v1/events/{slug}/reviews":                    {Summary: "Ulasan event", Res: service.EventReviews{}},
	"POST /api/v1/tickets/{id}/review":                     {Summary: "Beri ulasan (tiket sudah dipakai)", Req: service.ReviewInput{}, Status: 201},
	"GET /api/v1/organizer/finance/balance":                {Summary: "Saldo organizer", Res: service.BalanceView{}},
	"POST /api/v1/organizer/finance/payouts":               {Summary: "Minta pencairan (owner + MFA, Idempotency-Key)", Res: service.PayoutView{}, Status: 201},
	"GET /api/v1/organizer/finance/events/{id}/settlement": {Summary: "Laporan settlement event", Res: service.SettlementReport{}},
	"POST /api/v1/organizer/events/{id}/cancel":            {Summary: "Batalkan event + refund massal otomatis"},
	"POST /api/v1/organizer/events/{id}/reschedule":        {Summary: "Jadwal ulang event", Req: service.RescheduleInput{}},
	"POST /api/v1/organizer/events/{id}/comps":             {Summary: "Terbitkan tiket undangan", Req: service.CompInput{}, Res: service.CompView{}, Status: 201},
	"POST /api/v1/organizer/events/{id}/broadcasts":        {Summary: "Siaran pesan ke peserta", Req: service.BroadcastInput{}, Status: 201},
	"POST /api/v1/organizer/events/{id}/promos":            {Summary: "Buat kode promo", Req: service.PromoInput{}, Res: service.PromoView{}, Status: 201},
	"GET /api/v1/organizer/events/{id}/attendees":          {Summary: "Daftar peserta", Res: service.AttendeeView{}, List: true},
	"POST /api/v1/organizer/integrations/api-keys":         {Summary: "Buat API key (dilihat sekali)", Req: service.APIKeyInput{}, Res: service.APIKeyView{}, Status: 201},
	"POST /api/v1/organizer/integrations/webhooks":         {Summary: "Daftarkan webhook (secret dilihat sekali)", Req: service.WebhookInput{}, Res: service.WebhookView{}, Status: 201},
	"GET /api/v1/admin/finance/payouts":                    {Summary: "Antrean pencairan (finance)", Res: service.PayoutView{}, List: true},
	"GET /api/v1/admin/ops/events/{id}/control-room":       {Summary: "Control room onsale", Res: service.ControlRoom{}},
	"POST /api/v1/support/tickets":                         {Summary: "Buat tiket bantuan", Req: service.SupportInput{}, Res: service.SupportTicketView{}, Status: 201},
	"POST /api/v1/events/{slug}/photos":                    {Summary: "Unggah foto galeri (masuk moderasi)", Req: service.PhotoInput{}, Status: 201},
}

// Tag dari prefix path.
func tagOf(path string) string {
	p := strings.TrimPrefix(path, "/api/v1/")
	switch {
	case strings.HasPrefix(path, "/webhooks"):
		return "webhooks"
	case !strings.HasPrefix(path, "/api/v1/"):
		return "root"
	}
	seg := strings.Split(p, "/")
	switch seg[0] {
	case "admin", "organizer", "partner", "agent":
		if len(seg) > 1 {
			return seg[0] + "/" + seg[1]
		}
	}
	return seg[0]
}

func toOpenAPIPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if strings.HasPrefix(s, ":") {
			parts[i] = "{" + s[1:] + "}"
		} else if strings.HasPrefix(s, "*") {
			parts[i] = "{" + s[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

func pathParams(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			out = append(out, s[1:len(s)-1])
		}
	}
	return out
}

func opID(method, path string) string {
	prefix := ""
	if !strings.HasPrefix(path, "/api/v1/") {
		prefix = "root_" // halaman HTML/SEO, health, webhook — di luar /api/v1
	}
	r := strings.NewReplacer("/api/v1/", "", "/", "_", "{", "", "}", "", ".", "_", "-", "_")
	return strings.ToLower(method) + "_" + prefix + strings.Trim(r.Replace(path), "_")
}

// Build menyusun dokumen. authRequired memberi tahu apakah route menolak permintaan tanpa kredensial (dideteksi dengan probe).
func Build(routes []gin.RouteInfo, authRequired func(method, path string) bool) (*openapi3.T, error) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{Title: "TixGo API", Version: "1.0.0",
			Description: "Dibangkitkan dari router (internal/apidoc). Semua respons sukses dibungkus {data} (daftar: {data, meta}); galat {error:{code,message,details,request_id}}. Kode galat: docs/08-API-CONTRACT.md §2.",
			License:     &openapi3.License{Name: "Proprietary"}},
		Servers: openapi3.Servers{{URL: "http://localhost:8081"}},
		Components: &openapi3.Components{
			SecuritySchemes: openapi3.SecuritySchemes{
				"bearerAuth": &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}},
				"apiKey":     &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{Type: "apiKey", In: "header", Name: "X-API-Key"}},
			},
			Schemas: openapi3.Schemas{},
			Parameters: openapi3.ParametersMap{
				"IdempotencyKey": &openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "Idempotency-Key", In: "header", Required: true,
					Description: "8–128 karakter; permintaan ulang dengan kunci sama mengembalikan respons yang sama.", Schema: openapi3.NewStringSchema().NewRef()}},
			},
		},
		Paths: openapi3.NewPaths(),
	}
	errSchema := &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().WithProperty("error", openapi3.NewObjectSchema().
		WithProperty("code", openapi3.NewStringSchema()).WithProperty("message", openapi3.NewStringSchema()).
		WithProperty("details", openapi3.NewObjectSchema().WithAnyAdditionalProperties()).WithProperty("request_id", openapi3.NewStringSchema()))}
	doc.Components.Schemas["Error"] = errSchema
	doc.Components.Responses = openapi3.ResponseBodies{}
	for code, desc := range map[string]string{"400": "Validasi gagal (VALIDATION_FAILED)", "401": "Tidak terautentikasi", "403": "Dilarang", "404": "Tidak ditemukan", "409": "Konflik status", "422": "Aturan bisnis menolak", "429": "Terlalu banyak permintaan"} {
		d := desc
		doc.Components.Responses["Err"+code] = &openapi3.ResponseRef{Value: &openapi3.Response{Description: &d,
			Content: openapi3.Content{"application/json": &openapi3.MediaType{Schema: openapi3.NewSchemaRef("#/components/schemas/Error", nil)}}}}
	}
	// Satu generator BARU per tipe: skema bersarang di-inline (kin-openapi hanya mengekspor komponen untuk tipe siklik,
	// sehingga generator bersama menghasilkan $ref menggantung). Tipe puncak didaftarkan sebagai komponen bernama.
	ref := func(v any) *openapi3.SchemaRef {
		name := reflect.TypeOf(v).Name()
		if _, ok := doc.Components.Schemas[name]; !ok {
			s, err := openapi3gen.NewGenerator(openapi3gen.UseAllExportedFields()).NewSchemaRefForValue(v, nil)
			if err != nil || s == nil || s.Value == nil {
				return &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().WithAnyAdditionalProperties()}
			}
			doc.Components.Schemas[name] = &openapi3.SchemaRef{Value: s.Value}
		}
		return openapi3.NewSchemaRef("#/components/schemas/"+name, nil)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	seen := map[string]bool{}
	for _, r := range routes {
		path := toOpenAPIPath(r.Path)
		key := r.Method + " " + path
		if seen[key] {
			continue
		}
		seen[key] = true
		meta := Docs[key]
		op := openapi3.NewOperation()
		op.OperationID = opID(r.Method, path)
		op.Tags = []string{tagOf(path)}
		op.Summary = meta.Summary
		if op.Summary == "" {
			op.Summary = r.Method + " " + path
		}
		for _, p := range pathParams(path) {
			op.AddParameter(&openapi3.Parameter{Name: p, In: "path", Required: true, Schema: openapi3.NewStringSchema().NewRef()})
		}
		secured := authRequired(r.Method, r.Path)
		switch {
		case strings.HasPrefix(path, "/api/v1/partner/"):
			op.Security = &openapi3.SecurityRequirements{{"apiKey": {}}}
		case strings.HasPrefix(path, "/webhooks/"):
			op.Security = &openapi3.SecurityRequirements{}
			op.Description = "Diautentikasi lewat signature_key Midtrans (SHA512), bukan token."
		case secured:
			op.Security = &openapi3.SecurityRequirements{{"bearerAuth": {}}}
		default:
			op.Security = &openapi3.SecurityRequirements{}
		}
		if strings.Contains(path, "/admin/") || strings.Contains(path, "/organizer/") || strings.Contains(path, "/agent/") {
			op.Description = strings.TrimSpace(op.Description + " Butuh peran khusus (admin/finance/support/editor/organizer) dan, untuk aksi sensitif, sesi MFA.")
		}
		needsKey := meta.Summary != "" && strings.Contains(meta.Summary, "Idempotency-Key")
		if needsKey {
			op.AddParameter(&openapi3.Parameter{Name: "Idempotency-Key", In: "header", Required: true, Schema: openapi3.NewStringSchema().NewRef()})
		}
		if meta.Req != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			op.RequestBody = &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().WithJSONSchemaRef(ref(meta.Req))}
		} else if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			op.RequestBody = &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().WithJSONSchema(openapi3.NewObjectSchema().WithAnyAdditionalProperties())}
		}
		status := meta.Status
		if status == 0 {
			status = 200
			if r.Method == http.MethodDelete {
				status = 204
			}
		}
		okResp := openapi3.NewResponse().WithDescription("Sukses")
		if status != 204 {
			var data *openapi3.SchemaRef
			if meta.Res != nil {
				data = ref(meta.Res)
			} else {
				data = &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().WithAnyAdditionalProperties()}
			}
			body := openapi3.NewObjectSchema()
			if meta.List {
				arr := openapi3.NewArraySchema()
				arr.Items = data
				body.WithProperty("meta", openapi3.NewObjectSchema().
					WithProperty("page", openapi3.NewIntegerSchema()).WithProperty("per_page", openapi3.NewIntegerSchema()).WithProperty("total", openapi3.NewIntegerSchema()))
				body.Properties["data"] = openapi3.NewSchemaRef("", arr)
			} else {
				body.Properties = openapi3.Schemas{"data": data}
			}
			okResp = okResp.WithJSONSchema(body)
		}
		op.AddResponse(status, okResp)
		for _, code := range []string{"400", "404", "409", "422", "429"} {
			op.Responses.Set(code, &openapi3.ResponseRef{Ref: "#/components/responses/Err" + code})
		}
		if secured {
			op.Responses.Set("401", &openapi3.ResponseRef{Ref: "#/components/responses/Err401"})
			op.Responses.Set("403", &openapi3.ResponseRef{Ref: "#/components/responses/Err403"})
		}
		item := doc.Paths.Value(path)
		if item == nil {
			item = &openapi3.PathItem{}
			doc.Paths.Set(path, item)
		}
		item.SetOperation(r.Method, op)
	}
	return doc, nil
}
