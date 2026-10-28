// Load test for the Cloud Run app.
//
//   k6 run -e BASE_URL=https://... -e TARGET_PATH='/work?ms=200' -e VUS=50 -e DURATION=2m loadtest/k6.js
//
// Each virtual user sends requests back to back. With /work?ms=200 every
// request holds a slot for 200 ms, so 50 VUs keep ~50 requests in flight.
// Lower the service's concurrency (Terraform var) to see more instances.
import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL;
const TARGET_PATH = __ENV.TARGET_PATH || '/work?ms=200';
// Hard caps so a typo can't run up a bill.
const VUS = Math.min(parseInt(__ENV.VUS || '50', 10), 200);
const DURATION = __ENV.DURATION || '2m';

if (!BASE_URL) {
  throw new Error('BASE_URL is required');
}

export const options = {
  scenarios: {
    ramp: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: VUS },
        { duration: DURATION, target: VUS },
        { duration: '15s', target: 0 },
      ],
      gracefulRampDown: '5s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<1500'],
  },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const res = http.get(`${BASE_URL}${TARGET_PATH}`, { tags: { name: TARGET_PATH } });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
