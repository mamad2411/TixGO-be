// Package metrics: metrik Prometheus aplikasi (HTTP + alur bisnis) pada registry default (/metrics).
package metrics

import (
	"math"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpReqs = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_http_requests_total", Help: "Jumlah request HTTP."}, []string{"method", "route", "class"})
	httpDur  = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "tixgo_http_request_seconds", Help: "Latensi request HTTP.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"route"})

	Holds     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_holds_total", Help: "Hold dibuat (result: ok|full|error)."}, []string{"result"})
	Orders    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_orders_total", Help: "Order dibuat/berakhir."}, []string{"event"})
	Payments  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_payments_total", Help: "Hasil notifikasi pembayaran."}, []string{"result"})
	Refunds   = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_refunds_total", Help: "Refund per jalur/hasil."}, []string{"method", "result"})
	Scans     = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_gate_scans_total", Help: "Scan gate per hasil."}, []string{"result"})
	Webhooks  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "tixgo_webhook_total", Help: "Webhook (masuk: midtrans, keluar: organizer) per hasil."}, []string{"kind", "result"})
	JobErrors = promauto.NewCounter(prometheus.CounterOpts{Name: "tixgo_job_errors_total", Help: "Job gagal."})
	WSConns   = promauto.NewGauge(prometheus.GaugeOpts{Name: "tixgo_ws_connections", Help: "Koneksi WebSocket aktif."})
)

// ObserveHTTP dipanggil middleware. route = template (mis. /api/v1/orders/:id), bukan URL asli.
func ObserveHTTP(method, route string, status int, seconds float64) {
	class := "2xx"
	switch {
	case status >= 500:
		class = "5xx"
	case status >= 400:
		class = "4xx"
	case status >= 300:
		class = "3xx"
	}
	httpReqs.WithLabelValues(method, route, class).Inc()
	httpDur.WithLabelValues(route).Observe(seconds)
}

// HTTPSnapshot merangkum request sejak proses start: total, 5xx, dan perkiraan p95 (dari bucket histogram).
type HTTPStats struct {
	Total  float64 `json:"total"`
	Errors float64 `json:"errors_5xx"`
	P95Sec float64 `json:"p95_seconds"`
}

func HTTPSnapshot() HTTPStats {
	var st HTTPStats
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return st
	}
	type bkt struct{ le, n float64 }
	agg := map[float64]float64{}
	var count float64
	for _, mf := range mfs {
		switch mf.GetName() {
		case "tixgo_http_requests_total":
			for _, m := range mf.GetMetric() {
				v := m.GetCounter().GetValue()
				st.Total += v
				for _, l := range m.GetLabel() {
					if l.GetName() == "class" && l.GetValue() == "5xx" {
						st.Errors += v
					}
				}
			}
		case "tixgo_http_request_seconds":
			for _, m := range mf.GetMetric() {
				h := m.GetHistogram()
				count += float64(h.GetSampleCount())
				for _, b := range h.GetBucket() {
					agg[b.GetUpperBound()] += float64(b.GetCumulativeCount())
				}
			}
		}
	}
	if count == 0 {
		return st
	}
	var bs []bkt
	for le, n := range agg {
		if !math.IsInf(le, 1) {
			bs = append(bs, bkt{le, n})
		}
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].le < bs[j].le })
	for _, b := range bs {
		if b.n >= 0.95*count {
			st.P95Sec = b.le
			return st
		}
	}
	st.P95Sec = math.Inf(1)
	return st
}
