// Uji beban onsale: banyak pengguna berebut kuota GA. TIDAK BOLEH ada oversell (verifikasi: scripts/k6/verify-onsale.sh).
// Prasyarat (non-production!): PAYMENT_GATEWAY=fake RATE_LIMIT_DISABLED=true; `go run ./cmd/seed` lalu `go run ./cmd/seed -load 2000`.
// Jalankan: k6 run -e BASE_URL=http://localhost:8081 -e EVENT=senja-indie-fest -e USERS=2000 -e VUS=300 scripts/k6/onsale.js
import http from 'k6/http'
import { check, group, sleep } from 'k6'
import { Counter, Trend } from 'k6/metrics'

const BASE = __ENV.BASE_URL || 'http://localhost:8081'
const EVENT = __ENV.EVENT || 'senja-indie-fest'
const USERS = parseInt(__ENV.USERS || '500')
const API = `${BASE}/api/v1`
const J = { 'Content-Type': 'application/json' }

export const options = {
  scenarios: {
    onsale: { executor: 'ramping-vus', startVUs: 0, stages: [
      { duration: '15s', target: parseInt(__ENV.VUS || '200') },
      { duration: '45s', target: parseInt(__ENV.VUS || '200') },
      { duration: '10s', target: 0 } ] },
  },
  thresholds: {
    'checks{kind:hold}': ['rate>0.99'],      // hold: 201, 409 sold-out, atau 422 batas per akun (semua hasil bisnis sah); selain itu gagal
    'http_req_duration{kind:hold}': ['p(95)<500'],
    'http_req_duration{kind:order}': ['p(95)<800'],
    server_errors: ['count==0'],
  },
}

const serverErrors = new Counter('server_errors')
const holdsOK = new Counter('holds_ok')
const soldOut = new Counter('sold_out')
const paid = new Counter('orders_paid')
const holdMs = new Trend('hold_ms', true)

// 409/422 adalah hasil bisnis yang sah saat rebutan; hanya 5xx dan lainnya yang dianggap gagal.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 299 }, 400, 401, 404, 409, 422, 429))

export function setup() {
  const tokens = []
  for (let i = 1; i <= USERS; i++) {
    const email = `load${String(i).padStart(5, '0')}@tixgo.local`
    const r = http.post(`${API}/auth/login`, JSON.stringify({ email, password: 'Demo-Pass-123!' }), { headers: J })
    if (r.status === 200) tokens.push(r.json('data.access_token'))
  }
  const ev = http.get(`${API}/events/${EVENT}`).json('data')
  const tier = ev.tiers.find((t) => ev.mode === 'general') || ev.tiers[0]
  return { tokens, tier: tier.id }
}

export default function (d) {
  const token = d.tokens[(__VU - 1 + __ITER * 7) % d.tokens.length]
  const A = { headers: { ...J, Authorization: `Bearer ${token}` } }
  group('hold', () => {
    const t0 = Date.now()
    const r = http.post(`${API}/events/${EVENT}/holds`, JSON.stringify({ tier_id: d.tier, qty: 1 }), { ...A, tags: { kind: 'hold' } })
    holdMs.add(Date.now() - t0)
    check(r, { 'hold 201, penuh 409, atau batas per akun 422': (x) => x.status === 201 || x.status === 409 || x.status === 422 }, { kind: 'hold' })
    if (r.status >= 500) serverErrors.add(1)
    if (r.status === 409) { soldOut.add(1); sleep(1); return }
    if (r.status !== 201) return
    holdsOK.add(1)
    const key = `k6-${__VU}-${__ITER}-${Date.now()}`
    const o = http.post(`${API}/orders`, JSON.stringify({ hold_id: r.json('data.id') }), { headers: { ...A.headers, 'Idempotency-Key': key }, tags: { kind: 'order' } })
    if (o.status >= 500) serverErrors.add(1)
    if (o.status !== 201) return
    const oid = o.json('data.id')
    const p = http.post(`${API}/orders/${oid}/pay`, null, { headers: { ...A.headers, 'Idempotency-Key': `${key}-pay` }, tags: { kind: 'pay' } })
    if (p.status >= 500) serverErrors.add(1)
    const s = http.post(`${API}/dev/payments/settle`, JSON.stringify({ order_id: oid }), { ...A, tags: { kind: 'settle' } })
    if (s.status === 204) paid.add(1)
    else if (s.status >= 500) serverErrors.add(1)
  })
  sleep(0.3)
}
