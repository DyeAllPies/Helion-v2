// src/app/core/services/api.service.spec.ts
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';

import { ApiService } from './api.service';
import { Job, JobsPage, ClusterMetrics, AuditPage } from '../../shared/models';

// API response format (what the coordinator actually returns)
const mockApiNode = {
  id: 'n1', address: '10.0.0.1:9090',
  health: 'healthy', last_seen: new Date().toISOString(),
  running_jobs: 1, cpu_percent: 20, mem_percent: 40,
  registered_at: new Date().toISOString(),
};

const mockJob: Job = {
  id: 'j1', node_id: 'n1', command: 'echo', args: ['hi'],
  status: 'completed', created_at: new Date().toISOString(),
  exit_code: 0,
};

const mockPage: JobsPage = { jobs: [mockJob], total: 1, page: 0, size: 25 };

const mockMetrics: ClusterMetrics = {
  timestamp: new Date().toISOString(),
  total_nodes: 2, healthy_nodes: 2,
  total_jobs: 10, running_jobs: 1,
  pending_jobs: 0, completed_jobs: 9, failed_jobs: 0,
};

const mockAuditPage: AuditPage = {
  events: [{
    id: 'e1', type: 'security_violation',
    timestamp: new Date().toISOString(),
    actor: 'n1', message: 'seccomp violation',
  }],
  total: 1, page: 0, size: 50,
};

describe('ApiService', () => {
  let service: ApiService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        ApiService,
        provideHttpClient(),
        provideHttpClientTesting(),
      ],
    });
    service  = TestBed.inject(ApiService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => httpMock.verify());

  // ── Nodes ──────────────────────────────────────────────────────────────────

  it('getNodes() sends GET /nodes and maps API response to Node[]', () => {
    service.getNodes().subscribe(nodes => {
      expect(nodes.length).toBe(1);
      expect(nodes[0].node_id).toBe('n1');
      expect(nodes[0].healthy).toBeTrue();
      expect(nodes[0].address).toBe('10.0.0.1:9090');
    });
    const req = httpMock.expectOne(r => r.url.endsWith('/nodes'));
    expect(req.request.method).toBe('GET');
    // Flush the real API format: {nodes: [...], total: N}
    req.flush({ nodes: [mockApiNode], total: 1 });
  });

  // ── Jobs ───────────────────────────────────────────────────────────────────

  it('getJobs() sends GET /jobs with 1-indexed page and size params', () => {
    service.getJobs(0, 25).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs'));
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('page')).toBe('1');   // 0-indexed input → 1-indexed API
    expect(req.request.params.get('size')).toBe('25');
    expect(req.request.params.has('status')).toBeFalse();
    req.flush(mockPage);
  });

  it('getJobs() appends status param when provided', () => {
    service.getJobs(1, 10, 'failed').subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs'));
    expect(req.request.params.get('status')).toBe('failed');
    expect(req.request.params.get('page')).toBe('2');   // 1 → 2
    req.flush({ ...mockPage, jobs: [] });
  });

  it('getJob() sends GET /jobs/:id', () => {
    service.getJob('j1').subscribe(j => expect(j).toEqual(mockJob));
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs/j1'));
    expect(req.request.method).toBe('GET');
    req.flush(mockJob);
  });

  it('submitJob() sends POST /jobs with body', () => {
    const body = { id: 'j2', command: 'ls', args: ['-la'] };
    service.submitJob(body).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs') && r.method === 'POST');
    expect(req.request.body).toEqual(body);
    req.flush(mockJob);
  });

  // ── Metrics ────────────────────────────────────────────────────────────────

  it('getMetrics() sends GET /metrics', () => {
    service.getMetrics().subscribe(m => expect(m).toEqual(mockMetrics));
    const req = httpMock.expectOne(r => r.url.endsWith('/metrics'));
    expect(req.request.method).toBe('GET');
    req.flush(mockMetrics);
  });

  // ── Audit ──────────────────────────────────────────────────────────────────

  it('getAudit() sends GET /audit with 1-indexed page and size params', () => {
    service.getAudit(0, 50).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/audit'));
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('page')).toBe('1');   // 0 → 1
    expect(req.request.params.get('size')).toBe('50');
    expect(req.request.params.has('type')).toBeFalse();
    req.flush(mockAuditPage);
  });

  it('getAudit() appends type param when provided', () => {
    service.getAudit(0, 50, 'security_violation').subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/audit'));
    expect(req.request.params.get('type')).toBe('security_violation');
    req.flush(mockAuditPage);
  });

  it('getJobs() uses default page=1, size=25 when called with no args', () => {
    service.getJobs().subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs'));
    expect(req.request.params.get('page')).toBe('1');   // default 0 → 1
    expect(req.request.params.get('size')).toBe('25');
    expect(req.request.params.has('status')).toBeFalse();
    req.flush(mockPage);
  });

  it('getAudit() uses default page=1, size=50 when called with no args', () => {
    service.getAudit().subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/audit'));
    expect(req.request.params.get('page')).toBe('1');   // default 0 → 1
    expect(req.request.params.get('size')).toBe('50');
    expect(req.request.params.has('type')).toBeFalse();
    req.flush(mockAuditPage);
  });

  it('getAudit() does NOT append type param when omitted', () => {
    service.getAudit(0, 50).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/audit'));
    expect(req.request.params.has('type')).toBeFalse();
    req.flush(mockAuditPage);
  });

  // ── Analytics ──────────────────────────────────────────────────────────────

  it('getAnalyticsThroughput() sends GET /api/analytics/throughput with from/to', () => {
    const from = '2026-04-01T00:00:00Z';
    const to   = '2026-04-13T00:00:00Z';
    service.getAnalyticsThroughput(from, to).subscribe(resp => {
      expect(resp.data.length).toBe(1);
      expect(resp.from).toBe(from);
    });
    const req = httpMock.expectOne(r => r.url.endsWith('/api/analytics/throughput'));
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('from')).toBe(from);
    expect(req.request.params.get('to')).toBe(to);
    req.flush({
      from, to,
      data: [{
        hour: from, status: 'completed', job_count: 5,
        avg_duration_ms: 100, p95_duration_ms: 200,
      }],
    });
  });

  it('getAnalyticsNodeReliability() sends GET /api/analytics/node-reliability without params', () => {
    service.getAnalyticsNodeReliability().subscribe(resp => {
      expect(resp.data.length).toBe(1);
      expect(resp.data[0].node_id).toBe('n1');
    });
    const req = httpMock.expectOne(r => r.url.endsWith('/api/analytics/node-reliability'));
    expect(req.request.method).toBe('GET');
    expect(req.request.params.keys().length).toBe(0);
    req.flush({ data: [{
      node_id: 'n1', address: '10.0.0.1', jobs_completed: 5, jobs_failed: 1,
      failure_rate_pct: 16.67, times_stale: 0, times_revoked: 0,
    }] });
  });

  it('getAnalyticsRetryEffectiveness() sends GET /api/analytics/retry-effectiveness', () => {
    service.getAnalyticsRetryEffectiveness().subscribe(resp => {
      expect(resp.data.length).toBe(2);
    });
    const req = httpMock.expectOne(r => r.url.endsWith('/api/analytics/retry-effectiveness'));
    expect(req.request.method).toBe('GET');
    req.flush({ data: [
      { category: 'retried',       status: 'completed', job_count: 3, avg_duration_ms: 200 },
      { category: 'first_attempt', status: 'completed', job_count: 15, avg_duration_ms: 100 },
    ] });
  });

  it('getAnalyticsQueueWait() sends GET /api/analytics/queue-wait with from/to', () => {
    const from = '2026-04-01T00:00:00Z';
    const to   = '2026-04-13T00:00:00Z';
    service.getAnalyticsQueueWait(from, to).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/api/analytics/queue-wait'));
    expect(req.request.params.get('from')).toBe(from);
    expect(req.request.params.get('to')).toBe(to);
    req.flush({ from, to, data: [] });
  });

  it('getAnalyticsWorkflowOutcomes() sends GET /api/analytics/workflow-outcomes with from/to', () => {
    const from = '2026-04-01T00:00:00Z';
    const to   = '2026-04-13T00:00:00Z';
    service.getAnalyticsWorkflowOutcomes(from, to).subscribe(resp => {
      expect(resp.data.length).toBe(1);
    });
    const req = httpMock.expectOne(r => r.url.endsWith('/api/analytics/workflow-outcomes'));
    expect(req.request.params.get('from')).toBe(from);
    expect(req.request.params.get('to')).toBe(to);
    req.flush({ from, to, data: [{
      event_type: 'workflow.completed', day: '2026-04-13T00:00:00Z', count: 4,
    }] });
  });

  it('analytics requests hit HTTP GET method', () => {
    service.getAnalyticsThroughput('a', 'b').subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/api/analytics/throughput'));
    expect(req.request.method).toBe('GET');
    req.flush({ from: 'a', to: 'b', data: [] });
  });

  // ── Feature 32 — operator-cert lifecycle ──────────────────────────────────

  it('issueOperatorCert() sends POST /admin/operator-certs with body', () => {
    const body = { common_name: 'alice@ops', ttl_days: 90, p12_password: 'sekret!!' };
    service.issueOperatorCert(body).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs') && r.method === 'POST');
    expect(req.request.body).toEqual(body);
    req.flush({
      common_name: 'alice@ops',
      serial_hex: 'abcd',
      fingerprint_hex: 'beef',
      not_before: '2026-04-20T00:00:00Z',
      not_after: '2026-07-19T00:00:00Z',
      cert_pem: '---',
      key_pem: '---',
      p12_base64: 'AAA=',
      audit_notice: 'logged',
    });
  });

  it('revokeOperatorCert() sends POST /admin/operator-certs/{serial}/revoke', () => {
    service.revokeOperatorCert('abcd', { reason: 'laptop stolen' }).subscribe();
    const req = httpMock.expectOne(r =>
      r.url.endsWith('/admin/operator-certs/abcd/revoke') && r.method === 'POST');
    expect(req.request.body).toEqual({ reason: 'laptop stolen' });
    req.flush({
      serial_hex: 'abcd',
      revoked_at: '2026-04-20T00:00:00Z',
      revoked_by: 'user:root',
      reason: 'laptop stolen',
      idempotent: false,
    });
  });

  it('revokeOperatorCert() URL-encodes the serial', () => {
    service.revokeOperatorCert('ab/cd', { reason: 'x' }).subscribe();
    const req = httpMock.expectOne(r => r.url.indexOf('/admin/operator-certs/ab%2Fcd/revoke') !== -1);
    expect(req.request.method).toBe('POST');
    req.flush({
      serial_hex: 'ab/cd', revoked_at: '', revoked_by: '', idempotent: false,
    });
  });

  it('listRevocations() sends GET /admin/operator-certs/revocations', () => {
    service.listRevocations().subscribe(resp => {
      expect(resp.total).toBe(1);
      expect(resp.revocations[0].serial_hex).toBe('abcd');
    });
    const req = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs/revocations'));
    expect(req.request.method).toBe('GET');
    req.flush({
      total: 1,
      revocations: [{
        serial_hex: 'abcd',
        common_name: 'alice@ops',
        revoked_at: '2026-04-20T00:00:00Z',
        revoked_by: 'user:root',
        reason: 'test',
      }],
    });
  });
});
