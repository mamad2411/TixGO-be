package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
	"tixgo/backend/internal/platform"
	"tixgo/backend/internal/repository/sqlcgen"
)

// QueueService = waiting room & antrean adil (docs/04 §3, 20 M5, 10 §1).
//
//	Sebelum T0 semua peserta setara (pool). Saat T0 urutan ditentukan oleh HMAC-SHA256(seed, userID) —
//	seed di-commit (hash) sebelum dibuka dan diungkap sesudahnya sehingga urutan dapat diaudit.
//	Setelah T0, yang baru masuk diletakkan di belakang. Setiap tick, N peserta teratas diberi token admission
//	selama jendela beli; kapasitas aktif dibatasi max_concurrent.
type QueueService struct{ s *Services }

func newQueueService(s *Services) *QueueService { return &QueueService{s: s} }

// TickInterval = jeda tick admit (dipakai estimasi ETA).
const TickInterval = 2 * time.Second

// States
const (
	QNone        = "none"
	QWaitingRoom = "waiting_room"
	QQueued      = "queued"
	QAdmitted    = "admitted"
	QExpired     = "expired"
	QDone        = "done"
)

func qk(eventID, name string) string { return "wr:" + eventID + ":" + name }

type QueueStatus struct {
	State     string     `json:"state"`
	Position  int64      `json:"position,omitempty"`
	Ahead     int64      `json:"ahead,omitempty"`
	Total     int64      `json:"total,omitempty"`
	ETASec    int        `json:"eta_sec,omitempty"`
	OpensAt   *time.Time `json:"opens_at,omitempty"` // waktu antrean dibuka/diacak (T0)
	Token     string     `json:"token,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	PhaseID   string     `json:"phase_id,omitempty"`
	Rules     []string   `json:"rules,omitempty"`
	SeedHash  string     `json:"seed_hash,omitempty"`
}

// queueWindow menentukan fase tempat antrean berlaku sekarang untuk pengguna.
// Mengembalikan fase, dan error bila antrean belum dibuka/sudah tutup/tak berhak.
func (qs *QueueService) queueWindow(ctx context.Context, ev sqlcgen.Event, userID string) (*sqlcgen.SalePhase, error) {
	now := qs.s.now()
	phases, err := qs.s.Q.ListPhases(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	var candidate *sqlcgen.SalePhase
	var nextOpen *time.Time
	for i := range phases {
		p := phases[i]
		if !now.Before(p.EndsAt) {
			continue
		}
		open := p.StartsAt.Add(-time.Duration(ev.QueuePreOpenMin) * time.Minute)
		if p.QueueOpenAt != nil {
			open = *p.QueueOpenAt
		}
		if now.Before(open) {
			if nextOpen == nil || open.Before(*nextOpen) {
				o := open
				nextOpen = &o
			}
			continue
		}
		if p.Audience != "public" {
			has, err := qs.s.Q.HasPhaseAccess(ctx, sqlcgen.HasPhaseAccessParams{PhaseID: p.ID, UserID: userID})
			if err != nil {
				return nil, err
			}
			if !has {
				continue
			}
		}
		if candidate == nil || p.StartsAt.Before(candidate.StartsAt) {
			cp := p
			candidate = &cp
		}
	}
	if candidate != nil {
		return candidate, nil
	}
	e := apperr.New(409, apperr.CodeQueueClosed, "antrean belum dibuka atau sudah ditutup")
	if nextOpen != nil {
		e = e.With("opens_at", nextOpen.UTC().Format(time.RFC3339))
	}
	return nil, e
}

func (qs *QueueService) loadEvent(ctx context.Context, key string) (sqlcgen.Event, error) {
	ev, err := qs.s.Catalog.lookup(ctx, key)
	if err != nil || (ev.Status != stPublished && ev.Status != stPaused) {
		return ev, apperr.NotFound("event")
	}
	return ev, nil
}

// seedFor memastikan seed + commit hash ada SEBELUM T0 (idempoten).
func (qs *QueueService) seedFor(ctx context.Context, eventID string) (sqlcgen.QueueRun, error) {
	run, err := qs.s.Q.GetQueueRun(ctx, eventID)
	if err == nil {
		return run, nil
	}
	if !platform.IsNoRows(err) {
		return run, err
	}
	seed := hex.EncodeToString(auth.RandomBytes(32))
	sum := sha256.Sum256([]byte(seed))
	if err := qs.s.Q.EnsureQueueRun(ctx, sqlcgen.EnsureQueueRunParams{EventID: eventID, SeedHash: hex.EncodeToString(sum[:]), Seed: seed}); err != nil {
		return run, err
	}
	return qs.s.Q.GetQueueRun(ctx, eventID)
}

// Join memasukkan pengguna ke waiting room/antrean. Idempoten: satu akun = satu posisi.
func (qs *QueueService) Join(ctx context.Context, userID, eventKey string) (*QueueStatus, error) {
	ev, err := qs.loadEvent(ctx, eventKey)
	if err != nil {
		return nil, err
	}
	if !ev.QueueRequired {
		return nil, apperr.Conflict(apperr.CodeInvalidState, "event ini tidak memakai antrean")
	}
	if ev.SalesPaused {
		return nil, apperr.Conflict(apperr.CodeSalePaused, "penjualan sedang dijeda")
	}
	if ok, _, err := qs.s.Limiter.Allow(ctx, "qjoin:"+userID, 20, time.Minute); err == nil && !ok {
		return nil, apperr.TooMany("terlalu sering mencoba bergabung")
	}
	if _, err := qs.s.Auth.RequireVerified(ctx, qs.s.Q, userID); err != nil {
		return nil, err
	}
	if qs.s.Fraud.Decision(ctx, userID) == "block" {
		return nil, apperr.ForbiddenCode(apperr.CodeBlocked, "akses ditolak")
	}
	phase, err := qs.queueWindow(ctx, ev, userID)
	if err != nil {
		return nil, err
	}
	run, err := qs.seedFor(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	if st, known, err := qs.existing(ctx, ev, userID); err != nil {
		return nil, err
	} else if known {
		st.SeedHash = run.SeedHash
		return st, nil
	}
	qs.s.Fraud.RecordJoin(ctx, ev.ID, userID)
	now := qs.s.now()
	opened, _ := qs.s.Redis.Exists(ctx, qk(ev.ID, "opened")).Result()
	if opened == 0 && now.Before(phase.StartsAt) {
		if err := qs.s.Redis.SAdd(ctx, qk(ev.ID, "pool"), userID).Err(); err != nil {
			return nil, err
		}
		qs.s.Analytics.Track(ctx, ev.ID, "queue_join")
		return qs.Status(ctx, userID, eventKey)
	}
	// antrean sudah dibuka (T0 lewat): pastikan pengacakan dilakukan, lalu taruh di belakang
	if err := qs.open(ctx, ev); err != nil {
		return nil, err
	}
	seq, err := qs.s.Redis.Incr(ctx, qk(ev.ID, "seq")).Result()
	if err != nil {
		return nil, err
	}
	if err := qs.s.Redis.ZAdd(ctx, qk(ev.ID, "q"), redis.Z{Score: float64(seq), Member: userID}).Err(); err != nil {
		return nil, err
	}
	qs.s.Redis.Del(ctx, qk(ev.ID, "expired:"+userID))
	qs.s.Analytics.Track(ctx, ev.ID, "queue_join")
	return qs.Status(ctx, userID, eventKey)
}

// existing: apakah pengguna sudah punya posisi/status. known=true → kembalikan apa adanya.
func (qs *QueueService) existing(ctx context.Context, ev sqlcgen.Event, userID string) (*QueueStatus, bool, error) {
	st, err := qs.Status(ctx, userID, ev.ID)
	if err != nil {
		return nil, false, err
	}
	switch st.State {
	case QWaitingRoom, QQueued, QAdmitted:
		return st, true, nil
	}
	return st, false, nil
}

// open mengacak pool saat T0 (sekali; aman dipanggil berulang/paralel).
func (qs *QueueService) open(ctx context.Context, ev sqlcgen.Event) error {
	ok, err := qs.s.Redis.SetNX(ctx, qk(ev.ID, "opened"), qs.s.now().Format(time.RFC3339Nano), 0).Result()
	if err != nil {
		return err
	}
	if !ok {
		return nil // sudah dibuka (atau sedang dibuka proses lain)
	}
	run, err := qs.seedFor(ctx, ev.ID)
	if err != nil {
		qs.s.Redis.Del(ctx, qk(ev.ID, "opened"))
		return err
	}
	members, err := qs.s.Redis.SMembers(ctx, qk(ev.ID, "pool")).Result()
	if err != nil {
		qs.s.Redis.Del(ctx, qk(ev.ID, "opened"))
		return err
	}
	order := ShuffleOrder(run.Seed, members)
	if len(order) > 0 {
		z := make([]redis.Z, 0, len(order))
		for i, u := range order {
			z = append(z, redis.Z{Score: float64(i + 1), Member: u})
		}
		if err := qs.s.Redis.ZAdd(ctx, qk(ev.ID, "q"), z...).Err(); err != nil {
			qs.s.Redis.Del(ctx, qk(ev.ID, "opened"))
			return err
		}
	}
	qs.s.Redis.Set(ctx, qk(ev.ID, "seq"), len(order), 0)
	qs.s.Redis.Del(ctx, qk(ev.ID, "pool"))
	sorted := append([]string(nil), members...)
	sort.Strings(sorted)
	digest := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	if err := qs.s.Q.StartQueueRun(ctx, sqlcgen.StartQueueRunParams{EventID: ev.ID, StartedAt: ptr(qs.s.now()), Participants: len(order), ParticipantsDigest: ptr(hex.EncodeToString(digest[:]))}); err != nil {
		return err
	}
	qs.s.publish(ctx, "queue."+ev.ID, "queue.opened", map[string]any{"participants": len(order)})
	return nil
}

// ShuffleOrder mengurutkan peserta berdasarkan HMAC-SHA256(seed, userID) (deterministik & dapat diaudit).
func ShuffleOrder(seed string, users []string) []string {
	type kv struct{ k, u string }
	list := make([]kv, 0, len(users))
	for _, u := range users {
		m := hmac.New(sha256.New, []byte(seed))
		m.Write([]byte(u))
		list = append(list, kv{hex.EncodeToString(m.Sum(nil)), u})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].k != list[j].k {
			return list[i].k < list[j].k
		}
		return list[i].u < list[j].u
	})
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.u
	}
	return out
}

// Status melaporkan posisi/ETA/token pengguna.
func (qs *QueueService) Status(ctx context.Context, userID, eventKey string) (*QueueStatus, error) {
	ev, err := qs.loadEvent(ctx, eventKey)
	if err != nil {
		return nil, err
	}
	st := &QueueStatus{State: QNone}
	rules := []string{
		fmt.Sprintf("Maksimal %d tiket per akun", ev.MaxPerAccount),
		fmt.Sprintf("Setelah giliranmu tiba, kamu punya %d menit untuk memilih dan membayar", ev.AdmitWindowSec/60),
		"Urutan diacak adil saat penjualan dibuka — datang lebih awal tidak memberi keuntungan",
	}
	r := qs.s.Redis
	if run, err := qs.s.Q.GetQueueRun(ctx, ev.ID); err == nil {
		st.SeedHash = run.SeedHash
	}
	if n, _ := r.SIsMember(ctx, qk(ev.ID, "pool"), userID).Result(); n {
		st.State = QWaitingRoom
		st.Rules = rules
		st.Total, _ = r.SCard(ctx, qk(ev.ID, "pool")).Result()
		if phase, err := qs.queueWindow(ctx, ev, userID); err == nil {
			t := phase.StartsAt
			st.OpensAt, st.PhaseID = &t, phase.ID
		}
		return st, nil
	}
	if score, err := r.ZScore(ctx, qk(ev.ID, "active"), userID).Result(); err == nil {
		exp := time.UnixMilli(int64(score)).UTC()
		if exp.After(qs.s.now()) {
			st.State = QAdmitted
			st.ExpiresAt = &exp
			st.Token, _ = r.Get(ctx, "adm:"+ev.ID+":"+userID).Result()
			return st, nil
		}
	}
	if rank, err := r.ZRank(ctx, qk(ev.ID, "q"), userID).Result(); err == nil {
		st.State = QQueued
		st.Position = rank + 1
		st.Ahead = rank
		st.Total, _ = r.ZCard(ctx, qk(ev.ID, "q")).Result()
		rate := int64(ev.AdmitRate)
		if rate < 1 {
			rate = 1
		}
		st.ETASec = int(((rank + rate) / rate)) * int(TickInterval.Seconds())
		st.Rules = rules
		return st, nil
	}
	if n, _ := r.Exists(ctx, qk(ev.ID, "done:"+userID)).Result(); n > 0 {
		st.State = QDone
		return st, nil
	}
	if n, _ := r.Exists(ctx, qk(ev.ID, "expired:"+userID)).Result(); n > 0 {
		st.State = QExpired
	}
	return st, nil
}

// Leave keluar dari antrean (posisi hangus).
func (qs *QueueService) Leave(ctx context.Context, userID, eventKey string) error {
	ev, err := qs.loadEvent(ctx, eventKey)
	if err != nil {
		return err
	}
	pipe := qs.s.Redis.TxPipeline()
	pipe.SRem(ctx, qk(ev.ID, "pool"), userID)
	pipe.ZRem(ctx, qk(ev.ID, "q"), userID)
	pipe.ZRem(ctx, qk(ev.ID, "active"), userID)
	pipe.Del(ctx, "adm:"+ev.ID+":"+userID)
	_, err = pipe.Exec(ctx)
	return err
}

// FreeSlot melepas slot aktif (setelah order dibuat / hold dilepas / kedaluwarsa).
func (qs *QueueService) FreeSlot(ctx context.Context, eventID, userID string, done bool) {
	pipe := qs.s.Redis.TxPipeline()
	pipe.ZRem(ctx, qk(eventID, "active"), userID)
	pipe.Del(ctx, "adm:"+eventID+":"+userID)
	if done {
		pipe.Set(ctx, qk(eventID, "done:"+userID), "1", 6*time.Hour)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		qs.s.Log.Error("free slot gagal", "error", err)
	}
}

// CheckAdmission memverifikasi X-Admission-Token pada endpoint ber-antrean (docs/04 §3 aturan 3).
func (qs *QueueService) CheckAdmission(ctx context.Context, ev sqlcgen.Event, userID, token string) error {
	if !ev.QueueRequired {
		return nil
	}
	// hanya berlaku selama ada fase yang sedang berjalan/antrean terbuka
	if token == "" {
		return apperr.ForbiddenCode(apperr.CodeNotAdmitted, "kamu belum mendapat giliran; bergabunglah ke antrean")
	}
	u, e, err := qs.s.Tokens.ParseAdmission(token)
	if err != nil {
		if err == auth.ErrTokenExpired {
			return apperr.ForbiddenCode(apperr.CodeAdmissionExpired, "giliranmu habis; antre lagi")
		}
		return apperr.ForbiddenCode(apperr.CodeNotAdmitted, "token giliran tidak valid")
	}
	if u != userID || e != ev.ID {
		return apperr.ForbiddenCode(apperr.CodeNotAdmitted, "token giliran bukan milikmu/event ini")
	}
	cur, err := qs.s.Redis.Get(ctx, "adm:"+ev.ID+":"+userID).Result()
	if err != nil || cur != token {
		return apperr.ForbiddenCode(apperr.CodeAdmissionExpired, "giliranmu sudah berakhir; antre lagi")
	}
	return nil
}

// Tick memproses satu siklus admit untuk event: bersihkan yang kedaluwarsa, lalu admit N teratas.
// Mengembalikan jumlah yang di-admit. Aman dijalankan beberapa replika (kunci tick).
func (qs *QueueService) Tick(ctx context.Context, ev sqlcgen.Event) (int, error) {
	r := qs.s.Redis
	ok, err := r.SetNX(ctx, qk(ev.ID, "tick"), "1", 1500*time.Millisecond).Result()
	if err != nil || !ok {
		return 0, err
	}
	now := qs.s.now()
	// buka (acak) bila T0 tiba
	if opened, _ := r.Exists(ctx, qk(ev.ID, "opened")).Result(); opened == 0 {
		phases, err := qs.s.Q.ListPhases(ctx, ev.ID)
		if err != nil {
			return 0, err
		}
		for _, p := range phases {
			if !now.Before(p.StartsAt) && now.Before(p.EndsAt) {
				if err := qs.open(ctx, ev); err != nil {
					return 0, err
				}
				break
			}
		}
		if opened, _ := r.Exists(ctx, qk(ev.ID, "opened")).Result(); opened == 0 {
			return 0, nil
		}
	}
	// bersihkan yang kedaluwarsa
	exp, err := r.ZRangeArgs(ctx, redis.ZRangeArgs{Key: qk(ev.ID, "active"), Start: "-inf", Stop: strconv.FormatInt(now.UnixMilli(), 10), ByScore: true}).Result()
	if err != nil {
		return 0, err
	}
	if len(exp) > 0 {
		pipe := r.TxPipeline()
		for _, u := range exp {
			pipe.ZRem(ctx, qk(ev.ID, "active"), u)
			pipe.Set(ctx, qk(ev.ID, "expired:"+u), "1", time.Hour)
			pipe.Del(ctx, "adm:"+ev.ID+":"+u)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return 0, err
		}
		for _, u := range exp {
			qs.s.publish(ctx, "queue."+ev.ID+"."+u, "queue.expired", map[string]any{})
		}
	}
	active, err := r.ZCard(ctx, qk(ev.ID, "active")).Result()
	if err != nil {
		return 0, err
	}
	capLeft := int64(ev.MaxConcurrent) - active
	n := int64(ev.AdmitRate)
	if capLeft < n {
		n = capLeft
	}
	admitted := 0
	if n > 0 {
		popped, err := r.ZPopMin(ctx, qk(ev.ID, "q"), n).Result()
		if err != nil {
			return 0, err
		}
		window := time.Duration(ev.AdmitWindowSec) * time.Second
		for _, z := range popped {
			u := z.Member.(string)
			tok, exp, err := qs.s.Tokens.IssueAdmission(u, ev.ID, window)
			if err != nil {
				return admitted, err
			}
			pipe := r.TxPipeline()
			pipe.Set(ctx, "adm:"+ev.ID+":"+u, tok, window+time.Minute)
			pipe.ZAdd(ctx, qk(ev.ID, "active"), redis.Z{Score: float64(exp.UnixMilli()), Member: u})
			if _, err := pipe.Exec(ctx); err != nil {
				return admitted, err
			}
			qs.s.publish(ctx, "queue."+ev.ID+"."+u, "queue.admitted", map[string]any{"token": tok, "expires_at": exp})
			qs.s.Analytics.Track(ctx, ev.ID, "admitted")
			admitted++
		}
	}
	size, _ := r.ZCard(ctx, qk(ev.ID, "q")).Result()
	qs.s.publish(ctx, "queue."+ev.ID, "queue.tick", map[string]any{"waiting": size, "active": active + int64(admitted)})
	return admitted, nil
}

// TickAll menjalankan Tick untuk semua event ber-antrean yang jendelanya sedang relevan (worker, tiap ~2 dtk).
func (qs *QueueService) TickAll(ctx context.Context) (int, error) {
	rows, err := qs.s.Pool.Query(ctx, `SELECT e.* FROM events e WHERE e.queue_required AND e.status IN ('published','paused')
		AND EXISTS (SELECT 1 FROM sale_phases sp WHERE sp.event_id = e.id AND sp.ends_at > $1
		  AND COALESCE(sp.queue_open_at, sp.starts_at - make_interval(mins => e.queue_pre_open_min)) <= $1)`, qs.s.now())
	if err != nil {
		return 0, err
	}
	evs, err := pgxCollectEvents(rows)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, ev := range evs {
		if ev.SalesPaused {
			continue
		}
		n, err := qs.Tick(ctx, ev)
		if err != nil {
			qs.s.Log.Error("queue tick gagal", "event", ev.ID, "error", err)
			continue
		}
		total += n
	}
	return total, nil
}

// Proof = bukti keadilan setelah antrean dibuka: seed diungkap agar urutan dapat diverifikasi.
type QueueProof struct {
	EventID            string     `json:"event_id"`
	SeedHash           string     `json:"seed_hash"`
	Seed               *string    `json:"seed,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	Participants       int        `json:"participants"`
	ParticipantsDigest *string    `json:"participants_digest,omitempty"`
	Algorithm          string     `json:"algorithm"`
}

func (qs *QueueService) Proof(ctx context.Context, eventKey string) (*QueueProof, error) {
	ev, err := qs.loadEvent(ctx, eventKey)
	if err != nil {
		return nil, err
	}
	run, err := qs.s.Q.GetQueueRun(ctx, ev.ID)
	if err != nil {
		return nil, apperr.NotFound("data antrean")
	}
	p := &QueueProof{EventID: ev.ID, SeedHash: run.SeedHash, StartedAt: run.StartedAt, Participants: run.Participants,
		ParticipantsDigest: run.ParticipantsDigest, Algorithm: "urutan = sort_asc(hex(HMAC-SHA256(key=seed, msg=userID))); seed_hash = SHA256(seed)"}
	if run.StartedAt != nil { // seed hanya diungkap SETELAH pengacakan dilakukan
		seed := run.Seed
		p.Seed = &seed
	}
	return p, nil
}

// Stats untuk control room.
func (qs *QueueService) Stats(ctx context.Context, eventID string) (map[string]int64, error) {
	r := qs.s.Redis
	pool, _ := r.SCard(ctx, qk(eventID, "pool")).Result()
	q, _ := r.ZCard(ctx, qk(eventID, "q")).Result()
	active, _ := r.ZCard(ctx, qk(eventID, "active")).Result()
	return map[string]int64{"waiting_room": pool, "queued": q, "active": active}, nil
}
