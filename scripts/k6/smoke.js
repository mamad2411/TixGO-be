// Smoke/load awal: health & readiness. Skenario onsale (antrean/hold) ditulis bersama M5/M4 (docs/13 §5 T20).
// Jalankan: k6 run scripts/k6/smoke.js  (BASE_URL default http://localhost:8081)
import http from 'k6/http'
import { check, sleep } from 'k6'

export const options = {
  stages: [
    { duration: '10s', target: 20 },
    { duration: '20s', target: 50 },
    { duration: '10s', target: 0 },
  ],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<300'],
  },
}

const BASE = __ENV.BASE_URL || 'http://localhost:8081'

export default function () {
  const h = http.get(`${BASE}/healthz`)
  check(h, { 'healthz 200': (r) => r.status === 200 })
  const r = http.get(`${BASE}/readyz`)
  check(r, { 'readyz 200': (r2) => r2.status === 200 })
  sleep(0.2)
}
