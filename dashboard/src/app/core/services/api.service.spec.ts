// src/app/core/services/api.service.spec.ts
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';

import { ApiService } from './api.service';
import { Job, JobsPage, Node, ClusterMetrics, AuditPage } from '../../shared/models';

const mockNode: Node = {
  node_id: 'n1', address: '10.0.0.1:9090',
  healthy: true, last_seen: new Date().toISOString(),
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

  it('getNodes() sends GET /nodes', () => {
    service.getNodes().subscribe(nodes => expect(nodes).toEqual([mockNode]));
    const req = httpMock.expectOne(r => r.url.endsWith('/nodes'));
    expect(req.request.method).toBe('GET');
    req.flush([mockNode]);
  });

  // ── Jobs ───────────────────────────────────────────────────────────────────

  it('getJobs() sends GET /jobs with page and size params', () => {
    service.getJobs(0, 25).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs'));
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('page')).toBe('0');
    expect(req.request.params.get('size')).toBe('25');
    expect(req.request.params.has('status')).toBeFalse();
    req.flush(mockPage);
  });

  it('getJobs() appends status param when provided', () => {
    service.getJobs(1, 10, 'failed').subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/jobs'));
    expect(req.request.params.get('status')).toBe('failed');
    expect(req.request.params.get('page')).toBe('1');
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

  it('getAudit() sends GET /audit with page and size params', () => {
    service.getAudit(0, 50).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/audit'));
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('page')).toBe('0');
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

  it('getAudit() does NOT append type param when omitted', () => {
    service.getAudit(0, 50).subscribe();
    const req = httpMock.expectOne(r => r.url.endsWith('/audit'));
    expect(req.request.params.has('type')).toBeFalse();
    req.flush(mockAuditPage);
  });
});
