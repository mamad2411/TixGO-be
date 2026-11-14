package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/id"
	"tixgo/backend/internal/repository/sqlcgen"
	"tixgo/backend/internal/storage"
	"tixgo/backend/internal/svgsafe"
)

type pgxTx = pgx.Tx

// SeatmapService = denah kursi: generator grid, impor SVG, blokir kursi (docs/20 M11 AC2, M4).
type SeatmapService struct{ s *Services }

func newSeatmapService(s *Services) *SeatmapService { return &SeatmapService{s: s} }

const (
	maxSeatsPerEvent = 20000
	seatSpacing      = 20.0
	sectionGap       = 60.0
)

type SectionSpec struct {
	Name        string
	TierID      string
	Rows        int
	SeatsPerRow int
	Accessible  []string // label kursi aksesibel (opsional)
}

type SeatmapSummary struct {
	Sections int `json:"sections"`
	Seats    int `json:"seats"`
}

func rowLabel(i int) string {
	s := ""
	for i >= 0 {
		s = string(rune('A'+i%26)) + s
		i = i/26 - 1
	}
	return s
}

type seatRow struct {
	id, eventID, sectionID, tierID, row, no, label string
	x, y                                           float64
	accessible                                     bool
}

// replaceSeatmap menghapus denah lama (hanya bila tidak ada kursi ter-hold/terjual) lalu menyisipkan yang baru.
func (m *SeatmapService) replaceSeatmap(ctx context.Context, q *sqlcgen.Queries, tx pgx.Tx, ev sqlcgen.Event, sections []sqlcgen.Section, seats []seatRow) error {
	if ev.Mode != "seated" {
		return apperr.Conflict(apperr.CodeInvalidState, "denah hanya untuk event seated")
	}
	if ev.Status != stDraft && ev.Status != stApproved {
		return apperr.Conflict(apperr.CodeInvalidState, "denah hanya dapat diganti pada draft/approved (sebelum terbit)")
	}
	inUse, err := q.CountEventSeatsInUse(ctx, ev.ID)
	if err != nil {
		return err
	}
	if inUse > 0 {
		return apperr.Conflict(apperr.CodeInvalidState, "ada kursi ter-hold/terjual; denah tidak dapat diganti")
	}
	if len(seats) > maxSeatsPerEvent {
		return apperr.Validation(fmt.Sprintf("maksimal %d kursi per event", maxSeatsPerEvent)).With("seats", len(seats))
	}
	if err := q.DeleteEventSeatmap(ctx, ev.ID); err != nil {
		return err
	}
	for _, sec := range sections {
		if _, err := q.InsertSection(ctx, sqlcgen.InsertSectionParams{ID: sec.ID, EventID: sec.EventID, TierID: sec.TierID, Name: sec.Name, SvgRef: sec.SvgRef, Sort: sec.Sort}); err != nil {
			return err
		}
	}
	rows := make([][]any, 0, len(seats))
	for _, s := range seats {
		rows = append(rows, []any{s.id, s.eventID, s.sectionID, s.tierID, s.row, s.no, s.label, s.x, s.y, s.accessible, "available"})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"seats"},
		[]string{"id", "event_id", "section_id", "tier_id", "row_label", "seat_no", "label", "x", "y", "accessible", "status"}, pgx.CopyFromRows(rows))
	return err
}

func (m *SeatmapService) tiersByEvent(ctx context.Context, q *sqlcgen.Queries, eventID string) (map[string]sqlcgen.Tier, map[string]string, error) {
	tiers, err := q.ListTiers(ctx, eventID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]sqlcgen.Tier{}
	byName := map[string]string{}
	for _, t := range tiers {
		byID[t.ID] = t
		byName[strings.ToLower(t.Name)] = t.ID
	}
	return byID, byName, nil
}

// Generate membuat denah grid dari spesifikasi section.
func (m *SeatmapService) Generate(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, specs []SectionSpec) (*SeatmapSummary, error) {
	if len(specs) == 0 || len(specs) > 50 {
		return nil, apperr.Validation("sections 1–50").With("field", "sections")
	}
	var sum SeatmapSummary
	err := m.s.InTxRaw(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		ev, err := m.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		tiers, _, err := m.tiersByEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		var sections []sqlcgen.Section
		var seats []seatRow
		names := map[string]bool{}
		yOff := 0.0
		total := 0
		for i, sp := range specs {
			name := strings.TrimSpace(sp.Name)
			if l := len([]rune(name)); l < 1 || l > 40 || strings.Contains(name, "-") {
				return apperr.Validation("nama section 1–40 karakter tanpa tanda '-'").With("field", fmt.Sprintf("sections[%d].name", i))
			}
			if names[strings.ToLower(name)] {
				return apperr.Validation("nama section harus unik").With("field", fmt.Sprintf("sections[%d].name", i))
			}
			names[strings.ToLower(name)] = true
			if _, ok := tiers[sp.TierID]; !ok {
				return apperr.Validation("tier tidak ditemukan pada event ini").With("field", fmt.Sprintf("sections[%d].tier_id", i))
			}
			if sp.Rows < 1 || sp.Rows > 100 || sp.SeatsPerRow < 1 || sp.SeatsPerRow > 100 {
				return apperr.Validation("rows dan seats_per_row harus 1–100").With("field", fmt.Sprintf("sections[%d]", i))
			}
			total += sp.Rows * sp.SeatsPerRow
			if total > maxSeatsPerEvent {
				return apperr.Validation(fmt.Sprintf("maksimal %d kursi per event", maxSeatsPerEvent))
			}
			secID := id.New()
			tid := sp.TierID
			sections = append(sections, sqlcgen.Section{ID: secID, EventID: eventID, TierID: &tid, Name: name, Sort: i})
			acc := map[string]bool{}
			for _, l := range sp.Accessible {
				acc[l] = true
			}
			for r := 0; r < sp.Rows; r++ {
				rl := rowLabel(r)
				for c := 1; c <= sp.SeatsPerRow; c++ {
					label := fmt.Sprintf("%s-%s-%d", name, rl, c)
					seats = append(seats, seatRow{id: id.New(), eventID: eventID, sectionID: secID, tierID: sp.TierID, row: rl, no: strconv.Itoa(c), label: label,
						x: float64(c) * seatSpacing, y: yOff + float64(r)*seatSpacing, accessible: acc[label]})
				}
			}
			yOff += float64(sp.Rows)*seatSpacing + sectionGap
		}
		if err := m.replaceSeatmap(ctx, q, tx, ev, sections, seats); err != nil {
			return err
		}
		if ev.Status == stApproved {
			if _, err := q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stDraft, Note: strPtr("denah berubah; ajukan ulang")}); err != nil {
				return err
			}
		}
		sum = SeatmapSummary{Sections: len(sections), Seats: len(seats)}
		return m.s.Audit.Log(ctx, q, p, "seatmap.generate", "event", eventID, nil, map[string]any{"sections": sum.Sections, "seats": sum.Seats})
	})
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

// SVGKey = lokasi SVG denah tersanitasi.
func SVGKey(eventID string) string { return "seatmaps/" + eventID + ".svg" }

type svgSeat struct {
	section, tier, label, row, no string
	x, y                          float64
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func parseFloat(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }

// parseSVGSeats membaca kursi dari SVG tersanitasi: <g data-section data-tier><circle data-seat="SEC-ROW-NO" cx cy/></g>.
func parseSVGSeats(clean []byte) ([]svgSeat, error) {
	dec := xml.NewDecoder(bytes.NewReader(clean))
	type frame struct{ section, tier string }
	var stack []frame
	var out []svgSeat
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur := frame{}
			if len(stack) > 0 {
				cur = stack[len(stack)-1]
			}
			if v := attr(t, "data-section"); v != "" {
				cur.section = v
			}
			if v := attr(t, "data-tier"); v != "" {
				cur.tier = v
			}
			stack = append(stack, cur)
			if label := attr(t, "data-seat"); label != "" {
				s := svgSeat{section: cur.section, tier: cur.tier, label: label, row: attr(t, "data-row"), no: attr(t, "data-no")}
				switch t.Name.Local {
				case "circle", "ellipse":
					s.x, s.y = parseFloat(attr(t, "cx")), parseFloat(attr(t, "cy"))
				default:
					s.x, s.y = parseFloat(attr(t, "x")), parseFloat(attr(t, "y"))
				}
				if s.section == "" {
					parts := strings.Split(label, "-")
					if len(parts) >= 3 {
						s.section = strings.Join(parts[:len(parts)-2], "-")
					}
				}
				if s.row == "" || s.no == "" {
					parts := strings.Split(label, "-")
					if len(parts) >= 2 {
						s.row, s.no = parts[len(parts)-2], parts[len(parts)-1]
					}
				}
				out = append(out, s)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return out, nil
}

// ImportSVG menyanitasi SVG, membaca kursi, lalu mengganti denah event.
func (m *SeatmapService) ImportSVG(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, raw []byte) (*SeatmapSummary, error) {
	clean, err := svgsafe.Sanitize(raw)
	if err != nil {
		return nil, apperr.Validation("SVG ditolak: "+err.Error()).With("field", "svg")
	}
	parsed, err := parseSVGSeats(clean)
	if err != nil {
		return nil, apperr.Validation("SVG tidak dapat dibaca: "+err.Error()).With("field", "svg")
	}
	if len(parsed) == 0 {
		return nil, apperr.Validation("tidak ada kursi (elemen ber-atribut data-seat) di SVG").With("field", "svg")
	}
	if len(parsed) > maxSeatsPerEvent {
		return nil, apperr.Validation(fmt.Sprintf("maksimal %d kursi per event", maxSeatsPerEvent))
	}
	var sum SeatmapSummary
	err = m.s.InTxRaw(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		ev, err := m.s.Event.ownEvent(ctx, q, sc, eventID)
		if err != nil {
			return err
		}
		tiers, byName, err := m.tiersByEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		secIDs := map[string]string{}
		var sections []sqlcgen.Section
		var seats []seatRow
		seenLabel := map[string]bool{}
		var dups, noTier []string
		for _, s := range parsed {
			if s.section == "" {
				return apperr.Validation("kursi tanpa section (gunakan data-section pada <g> atau label SECTION-ROW-NO)").With("seat", s.label)
			}
			if seenLabel[strings.ToLower(s.label)] {
				dups = append(dups, s.label)
				continue
			}
			seenLabel[strings.ToLower(s.label)] = true
			tid := ""
			if _, ok := tiers[s.tier]; ok {
				tid = s.tier
			} else if v, ok := byName[strings.ToLower(s.tier)]; ok {
				tid = v
			}
			if tid == "" {
				noTier = append(noTier, s.label)
				continue
			}
			key := strings.ToLower(s.section)
			secID, ok := secIDs[key]
			if !ok {
				secID = id.New()
				secIDs[key] = secID
				t := tid
				sections = append(sections, sqlcgen.Section{ID: secID, EventID: eventID, TierID: &t, Name: s.section, Sort: len(sections)})
			}
			seats = append(seats, seatRow{id: id.New(), eventID: eventID, sectionID: secID, tierID: tid, row: s.row, no: s.no, label: s.label, x: s.x, y: s.y})
		}
		if len(dups) > 0 {
			return apperr.Unprocessable(apperr.CodeValidation, "label kursi duplikat").With("duplicates", firstN(dups, 20))
		}
		if len(noTier) > 0 {
			return apperr.Unprocessable(apperr.CodeValidation, "tier pada data-tier tidak cocok dengan tier event").With("seats", firstN(noTier, 20))
		}
		if err := m.replaceSeatmap(ctx, q, tx, ev, sections, seats); err != nil {
			return err
		}
		if err := m.s.Store.Put(ctx, SVGKey(eventID), bytes.NewReader(clean), int64(len(clean)), "image/svg+xml"); err != nil {
			return err
		}
		if ev.Status == stApproved {
			if _, err := q.SetEventStatus(ctx, sqlcgen.SetEventStatusParams{ID: eventID, Status: stDraft, Note: strPtr("denah berubah; ajukan ulang")}); err != nil {
				return err
			}
		}
		sum = SeatmapSummary{Sections: len(sections), Seats: len(seats)}
		return m.s.Audit.Log(ctx, q, p, "seatmap.import_svg", "event", eventID, nil, map[string]any{"sections": sum.Sections, "seats": sum.Seats})
	})
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// SVG membaca SVG tersanitasi yang tersimpan (bila ada).
func (m *SeatmapService) SVG(ctx context.Context, eventID string) ([]byte, error) {
	rc, _, err := m.s.Store.Get(ctx, SVGKey(eventID))
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, apperr.NotFound("denah SVG")
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// SetBlocked memblokir/membuka kursi tersedia (kursi untuk panggung, sponsor, dsb.).
func (m *SeatmapService) SetBlocked(ctx context.Context, p *auth.Principal, sc OrgScope, eventID string, seatIDs []string, blocked bool) (int, error) {
	if len(seatIDs) == 0 || len(seatIDs) > 5000 {
		return 0, apperr.Validation("seat_ids 1–5000").With("field", "seat_ids")
	}
	status := "available"
	if blocked {
		status = "blocked"
	}
	var n int64
	err := m.s.InTx(ctx, func(q *sqlcgen.Queries) error {
		if _, err := m.s.Event.ownEvent(ctx, q, sc, eventID); err != nil {
			return err
		}
		var err error
		n, err = q.SetSeatsBlocked(ctx, sqlcgen.SetSeatsBlockedParams{NewStatus: status, EventID: eventID, Ids: seatIDs})
		if err != nil {
			return err
		}
		if err := q.InsertOutbox(ctx, sqlcgen.InsertOutboxParams{Channel: "event." + eventID, Topic: "seat.updated", Payload: mustJSON(map[string]any{"seat_ids": seatIDs, "status": status})}); err != nil {
			return err
		}
		return m.s.Audit.Log(ctx, q, p, "seat.block", "event", eventID, nil, map[string]any{"status": status, "count": n})
	})
	return int(n), err
}

// ---- geometri publik ----

type SeatGeom struct {
	ID         string  `json:"id"`
	SectionID  string  `json:"section_id"`
	TierID     string  `json:"tier_id"`
	Row        string  `json:"row"`
	No         string  `json:"no"`
	Label      string  `json:"label"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Accessible bool    `json:"accessible"`
}

type SectionGeom struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	TierID *string `json:"tier_id"`
}

type Seatmap struct {
	Sections []SectionGeom `json:"sections"`
	Seats    []SeatGeom    `json:"seats"`
	HasSVG   bool          `json:"has_svg"`
}

func (m *SeatmapService) Geometry(ctx context.Context, eventID string) (*Seatmap, error) {
	secs, err := m.s.Q.ListSections(ctx, eventID)
	if err != nil {
		return nil, err
	}
	rows, err := m.s.Q.ListSeatsGeometry(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := &Seatmap{Sections: make([]SectionGeom, 0, len(secs)), Seats: make([]SeatGeom, 0, len(rows))}
	for _, s := range secs {
		out.Sections = append(out.Sections, SectionGeom{ID: s.ID, Name: s.Name, TierID: s.TierID})
	}
	for _, r := range rows {
		g := SeatGeom{ID: r.ID, SectionID: r.SectionID, TierID: r.TierID, Row: r.RowLabel, No: r.SeatNo, Label: r.Label, Accessible: r.Accessible}
		if r.X != nil {
			g.X = *r.X
		}
		if r.Y != nil {
			g.Y = *r.Y
		}
		out.Seats = append(out.Seats, g)
	}
	sort.SliceStable(out.Seats, func(i, j int) bool { return false })
	_, _, err = m.s.Store.Get(ctx, SVGKey(eventID))
	out.HasSVG = err == nil
	return out, nil
}

// SeatStatuses = kursi yang TIDAK tersedia (ditahan/terjual/diblokir). Kursi lain = tersedia.
// Dipakai peta kursi publik untuk menandai kursi terisi sebelum pembeli mencoba menahannya.
type SeatStatuses struct {
	Held    []string `json:"held"`
	Sold    []string `json:"sold"`
	Blocked []string `json:"blocked"`
}

func (m *SeatmapService) Statuses(ctx context.Context, eventID string) (*SeatStatuses, error) {
	rows, err := m.s.Pool.Query(ctx, `SELECT id, status FROM seats WHERE event_id = $1 AND status <> 'available'`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &SeatStatuses{Held: []string{}, Sold: []string{}, Blocked: []string{}}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		switch st {
		case "held":
			out.Held = append(out.Held, id)
		case "sold":
			out.Sold = append(out.Sold, id)
		case "blocked":
			out.Blocked = append(out.Blocked, id)
		}
	}
	return out, rows.Err()
}
