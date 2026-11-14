package apitest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"testing"
	"time"
)

// queueEvent: event GA ber-antrean; fase publik mulai 10 menit lagi (waiting room sudah terbuka karena pre-open 30 menit).
func (e *Env) queueEvent(admitRate int) (o *Org, evID, slug, tier string) {
	e.T.Helper()
	o, evID, slug, tier = e.publishedGA(100000, 50)
	e.Exec("DELETE FROM sale_phases WHERE event_id=$1", evID)
	e.Exec("UPDATE events SET queue_required=true, admit_rate=$2, max_concurrent=100, admit_window_sec=120 WHERE id=$1", evID, admitRate)
	e.Exec("INSERT INTO sale_phases(id,event_id,name,audience,starts_at,ends_at) VALUES ($1,$2,'Umum','public',now()+interval '10 minutes',now()+interval '20 days')", newID(), evID)
	return
}

func (e *Env) tickQueue(evID string) int {
	e.T.Helper()
	e.Redis.Del(e.ctx, "wr:"+evID+":tick") // kunci tick memakai TTL waktu nyata
	n, err := e.Svc.Queue.TickAll(e.ctx)
	if err != nil {
		e.T.Fatal(err)
	}
	return n
}

func TestFairQueueWaitingRoomShuffleProofAndAdmission(t *testing.T) { // T09, T10, T11 + keadilan
	e := NewEnv(t)
	_, evID, slug, tier := e.queueEvent(2)
	const N = 6
	users := make([]*Client, N)
	for i := range users {
		users[i] = e.Verified("Antre")
		var st struct{ State string }
		users[i].POST(api+"/events/"+slug+"/queue/join", nil).Expect(200).Data(&st)
		if st.State != "waiting_room" {
			t.Fatalf("sebelum T0 → waiting_room: %s", st.State)
		}
	}
	// T09: gabung dua kali (2 tab) = satu posisi
	users[0].POST(api+"/events/"+slug+"/queue/join", nil).Expect(200)
	if n, _ := e.Redis.SCard(e.ctx, "wr:"+evID+":pool").Result(); n != N {
		t.Fatalf("pool %d, harus %d", n, N)
	}
	// T10: tanpa token admission → NOT_ADMITTED
	users[0].POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}).ExpectErr(403, "NOT_ADMITTED")
	users[0].POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}, "X-Admission-Token", "sampah").ExpectErr(403, "NOT_ADMITTED")
	// seed baru diungkap setelah pengacakan; sebelum itu hanya hash
	var pf struct {
		SeedHash string  `json:"seed_hash"`
		Seed     *string `json:"seed"`
	}
	e.Anon().GET(api + "/events/" + slug + "/queue/proof").Expect(200).Data(&pf)
	if pf.Seed != nil || pf.SeedHash == "" {
		t.Fatalf("seed tidak boleh terbuka sebelum T0: %+v", pf)
	}

	// T0 tiba → acak & admit 2 per tick
	e.Clock.Advance(11 * time.Minute)
	if got := e.tickQueue(evID); got != 2 {
		t.Fatalf("admit 2 per tick: %d", got)
	}
	// bukti keadilan: urutan = sort(HMAC(seed,userID)); verifikasi independen
	e.Anon().GET(api + "/events/" + slug + "/queue/proof").Expect(200).Data(&pf)
	if pf.Seed == nil {
		t.Fatal("seed harus terbuka setelah acak")
	}
	sum := sha256.Sum256([]byte(*pf.Seed))
	if hex.EncodeToString(sum[:]) != pf.SeedHash {
		t.Fatal("hash(seed) harus sama dengan komitmen sebelum T0")
	}
	type kv struct{ k, id string }
	var want []kv
	for _, u := range users {
		m := hmac.New(sha256.New, []byte(*pf.Seed))
		m.Write([]byte(u.UserID))
		want = append(want, kv{hex.EncodeToString(m.Sum(nil)), u.UserID})
	}
	sort.Slice(want, func(i, j int) bool { return want[i].k < want[j].k })
	pos := map[string]int{}
	for _, u := range users {
		var st struct {
			State    string
			Position int
			Token    string
		}
		u.GET(api + "/events/" + slug + "/queue/status").Expect(200).Data(&st)
		switch st.State {
		case "admitted":
			pos[u.UserID] = 0
		case "queued":
			pos[u.UserID] = st.Position
		default:
			t.Fatalf("state: %s", st.State)
		}
	}
	// 2 teratas menurut urutan terverifikasi harus admitted (posisi 0), sisanya berurutan 1..4
	for i, w := range want {
		if i < 2 && pos[w.id] != 0 {
			t.Fatalf("peserta urutan %d harus admitted", i+1)
		}
		if i >= 2 && pos[w.id] != i-1 {
			t.Fatalf("peserta urutan %d harus posisi %d, dapat %d", i+1, i-1, pos[w.id])
		}
	}
	byID := map[string]*Client{}
	for _, u := range users {
		byID[u.UserID] = u
	}
	adm := byID[want[0].id]
	other := byID[want[1].id]
	queued := byID[want[2].id]
	var st struct{ Token string }
	adm.GET(api + "/events/" + slug + "/queue/status").Data(&st)
	if st.Token == "" {
		t.Fatal("admitted memegang token")
	}
	// queued belum punya token → ditolak; token orang lain → ditolak
	queued.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}, "X-Admission-Token", st.Token).ExpectErr(403, "NOT_ADMITTED")
	other.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}, "X-Admission-Token", st.Token).ExpectErr(403, "NOT_ADMITTED")
	adm.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}, "X-Admission-Token", st.Token).Expect(201)

	// T11: token kedaluwarsa → ADMISSION_EXPIRED
	var st2 struct{ Token string }
	other.GET(api + "/events/" + slug + "/queue/status").Data(&st2)
	e.Clock.Advance(3 * time.Minute)
	other = e.Login(other.Email)
	other.POST(api+"/events/"+slug+"/holds", M{"tier_id": tier, "qty": 1}, "X-Admission-Token", st2.Token).ExpectErr(403, "ADMISSION_EXPIRED")
	// tick membersihkan yang kedaluwarsa → status expired; bisa antre ulang (di belakang)
	e.tickQueue(evID)
	other.GET(api + "/events/" + slug + "/queue/status").Expect(200).Data(&st)
}

func TestQueueMaxConcurrentAndLeave(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, _ := e.queueEvent(5)
	e.Exec("UPDATE events SET max_concurrent=2 WHERE id=$1", evID)
	var us []*Client
	for i := 0; i < 4; i++ {
		u := e.Verified("Sabar")
		u.POST(api+"/events/"+slug+"/queue/join", nil).Expect(200)
		us = append(us, u)
	}
	e.Clock.Advance(11 * time.Minute)
	if got := e.tickQueue(evID); got != 2 {
		t.Fatalf("dibatasi max_concurrent=2 walau rate 5: %d", got)
	}
	if got := e.tickQueue(evID); got != 0 {
		t.Fatalf("kapasitas penuh → 0: %d", got)
	}
	// yang keluar antrean kehilangan posisi
	var gone *Client
	for _, u := range us {
		var st struct{ State string }
		u.GET(api + "/events/" + slug + "/queue/status").Data(&st)
		if st.State == "queued" {
			gone = u
			break
		}
	}
	gone.DELETE(api + "/events/" + slug + "/queue").Expect(204)
	var st struct{ State string }
	gone.GET(api + "/events/" + slug + "/queue/status").Expect(200).Data(&st)
	if st.State != "none" {
		t.Fatalf("setelah keluar: %s", st.State)
	}
}

func TestQueueClosedBeforeOpenAndPausedSale(t *testing.T) {
	e := NewEnv(t)
	_, evID, slug, _ := e.queueEvent(5)
	e.Exec("UPDATE sale_phases SET starts_at=now()+interval '3 hours' WHERE event_id=$1", evID)
	u := e.Verified("Kepagian")
	u.POST(api+"/events/"+slug+"/queue/join", nil).ExpectErr(409, "QUEUE_CLOSED")
	e.Exec("UPDATE sale_phases SET starts_at=now()+interval '10 minutes' WHERE event_id=$1", evID)
	e.Exec("UPDATE events SET sales_paused=true, sales_paused_reason='uji' WHERE id=$1", evID)
	u.POST(api+"/events/"+slug+"/queue/join", nil).ExpectErr(409, "SALE_PAUSED")
	// event tanpa antrean menolak join
	_, _, slug2, _ := e.publishedGA(100000, 5)
	u.POST(api+"/events/"+slug2+"/queue/join", nil).ExpectErr(409, "INVALID_STATE")
	unv := e.Register(uniqEmail("unv"), "Belum Verif")
	e.Exec("UPDATE events SET sales_paused=false WHERE id=$1", evID)
	unv.POST(api+"/events/"+slug+"/queue/join", nil).ExpectErr(403, "EMAIL_NOT_VERIFIED")
}
