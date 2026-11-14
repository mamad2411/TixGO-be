// Package jobs = antrean tugas latar (asynq) di balik antarmuka kecil agar service tidak bergantung
// pada asynq dan tes dapat menjalankan tugas secara sinkron (Inline).
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hibiken/asynq"
)

// Handler memproses satu tugas. Mengembalikan error => retry (kecuali melewati batas).
type Handler func(ctx context.Context, payload []byte) error

// Options per-enqueue.
type Options struct {
	ProcessIn time.Duration
	UniqueKey string // tugas dengan kunci sama tidak diantrekan ganda selama UniqueTTL
	UniqueTTL time.Duration
	MaxRetry  int
	Queue     string
}

type Option func(*Options)

func In(d time.Duration) Option { return func(o *Options) { o.ProcessIn = d } }
func Unique(key string, ttl time.Duration) Option {
	return func(o *Options) { o.UniqueKey, o.UniqueTTL = key, ttl }
}
func MaxRetry(n int) Option   { return func(o *Options) { o.MaxRetry = n } }
func OnQueue(q string) Option { return func(o *Options) { o.Queue = q } }

// Enqueuer = sisi produsen.
type Enqueuer interface {
	Enqueue(ctx context.Context, typ string, payload any, opts ...Option) error
}

// Registry = sisi konsumen: peta tipe tugas → handler. Dipakai worker asynq dan Inline.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

func (r *Registry) Handle(typ string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[typ]; dup {
		panic("jobs: handler ganda untuk " + typ)
	}
	r.handlers[typ] = h
}

func (r *Registry) get(typ string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[typ]
	return h, ok
}

func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for k := range r.handlers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func marshal(payload any) ([]byte, error) {
	switch v := payload.(type) {
	case nil:
		return nil, nil
	case []byte:
		return v, nil
	default:
		return json.Marshal(payload)
	}
}

// ---- asynq ----

type AsynqClient struct{ c *asynq.Client }

func NewAsynqClient(opt asynq.RedisClientOpt) *AsynqClient {
	return &AsynqClient{c: asynq.NewClient(opt)}
}
func (a *AsynqClient) Close() error { return a.c.Close() }

func (a *AsynqClient) Enqueue(ctx context.Context, typ string, payload any, opts ...Option) error {
	b, err := marshal(payload)
	if err != nil {
		return fmt.Errorf("jobs: marshal %s: %w", typ, err)
	}
	var o Options
	for _, f := range opts {
		f(&o)
	}
	ao := []asynq.Option{}
	if o.ProcessIn > 0 {
		ao = append(ao, asynq.ProcessIn(o.ProcessIn))
	}
	if o.UniqueKey != "" {
		ttl := o.UniqueTTL
		if ttl <= 0 {
			ttl = time.Minute
		}
		ao = append(ao, asynq.Unique(ttl), asynq.TaskID(typ+":"+o.UniqueKey))
	}
	if o.MaxRetry > 0 {
		ao = append(ao, asynq.MaxRetry(o.MaxRetry))
	}
	if o.Queue != "" {
		ao = append(ao, asynq.Queue(o.Queue))
	}
	_, err = a.c.EnqueueContext(ctx, asynq.NewTask(typ, b), ao...)
	if err != nil && (isDup(err)) {
		return nil // tugas unik sudah ada: bukan error
	}
	return err
}

func isDup(err error) bool {
	return err != nil && (errorsIs(err, asynq.ErrDuplicateTask) || errorsIs(err, asynq.ErrTaskIDConflict))
}

// AsynqServer membungkus asynq.Server dengan registry.
type AsynqServer struct {
	srv *asynq.Server
	mux *asynq.ServeMux
}

func NewAsynqServer(opt asynq.RedisClientOpt, reg *Registry, concurrency int, log *slog.Logger) *AsynqServer {
	srv := asynq.NewServer(opt, asynq.Config{
		Concurrency: concurrency,
		Queues:      map[string]int{"critical": 6, "default": 3, "low": 1},
		ErrorHandler: asynq.ErrorHandlerFunc(func(_ context.Context, t *asynq.Task, err error) {
			log.Error("job gagal", "type", t.Type(), "error", err)
		}),
	})
	mux := asynq.NewServeMux()
	for _, typ := range reg.Types() {
		h, _ := reg.get(typ)
		mux.HandleFunc(typ, func(ctx context.Context, t *asynq.Task) error { return h(ctx, t.Payload()) })
	}
	return &AsynqServer{srv: srv, mux: mux}
}

func (s *AsynqServer) Run() error { return s.srv.Run(s.mux) }
func (s *AsynqServer) Shutdown()  { s.srv.Shutdown() }

// ---- Inline (tes & mode tanpa worker) ----

type pending struct {
	typ     string
	payload []byte
	at      time.Time
}

// Inline menjalankan tugas langsung saat Enqueue. Tugas bertunda (In) disimpan sampai RunDue.
type Inline struct {
	reg  *Registry
	now  func() time.Time
	mu   sync.Mutex
	late []pending
	seen map[string]bool
	// Errors mengumpulkan error handler agar tes dapat memeriksanya.
	Errors []error
}

func NewInline(reg *Registry, now func() time.Time) *Inline {
	return &Inline{reg: reg, now: now, seen: map[string]bool{}}
}

func (in *Inline) Enqueue(ctx context.Context, typ string, payload any, opts ...Option) error {
	b, err := marshal(payload)
	if err != nil {
		return err
	}
	var o Options
	for _, f := range opts {
		f(&o)
	}
	if o.UniqueKey != "" {
		in.mu.Lock()
		k := typ + ":" + o.UniqueKey
		if in.seen[k] {
			in.mu.Unlock()
			return nil
		}
		in.seen[k] = true
		in.mu.Unlock()
	}
	if o.ProcessIn > 0 {
		in.mu.Lock()
		in.late = append(in.late, pending{typ, b, in.now().Add(o.ProcessIn)})
		in.mu.Unlock()
		return nil
	}
	return in.run(ctx, typ, b)
}

func (in *Inline) run(ctx context.Context, typ string, b []byte) error {
	h, ok := in.reg.get(typ)
	if !ok {
		return fmt.Errorf("jobs: tidak ada handler untuk %q", typ)
	}
	if err := h(ctx, b); err != nil {
		in.mu.Lock()
		in.Errors = append(in.Errors, fmt.Errorf("%s: %w", typ, err))
		in.mu.Unlock()
		return nil // seperti asynq: kegagalan tugas tidak menggagalkan produsen
	}
	return nil
}

// RunDue menjalankan tugas bertunda yang sudah jatuh tempo; mengembalikan jumlahnya.
func (in *Inline) RunDue(ctx context.Context) int {
	in.mu.Lock()
	var due, rest []pending
	for _, p := range in.late {
		if !p.at.After(in.now()) {
			due = append(due, p)
		} else {
			rest = append(rest, p)
		}
	}
	in.late = rest
	in.mu.Unlock()
	for _, p := range due {
		_ = in.run(ctx, p.typ, p.payload)
	}
	return len(due)
}

// Pending = jumlah tugas bertunda.
func (in *Inline) Pending() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.late)
}

// Discard mengabaikan semua enqueue (dipakai bila jobs tidak diperlukan).
type Discard struct{}

func (Discard) Enqueue(context.Context, string, any, ...Option) error { return nil }

// Decode mendekode payload JSON.
func Decode[T any](b []byte) (T, error) {
	var v T
	err := json.Unmarshal(b, &v)
	return v, err
}
