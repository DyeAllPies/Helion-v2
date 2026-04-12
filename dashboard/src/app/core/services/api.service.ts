// src/app/core/services/api.service.ts
//
// Thin wrapper around Angular HttpClient.
// All requests go to environment.coordinatorUrl.
// In production Nginx proxies /api and /ws on the same origin, so coordinatorUrl is ''.

import { Injectable } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';
import { environment } from '../../../environments/environment';
import {
  Job, JobsPage, Node, ClusterMetrics, AuditPage, SubmitJobRequest
} from '../../shared/models';

// Raw API response shapes (may differ from dashboard models)
interface ApiNodeInfo {
  id: string;
  health: string;        // "healthy" | "unhealthy"
  last_seen: string;
  running_jobs: number;
  address: string;
  registered_at?: string;
  cpu_percent?: number;
  mem_percent?: number;
}
interface ApiNodeListResponse {
  nodes: ApiNodeInfo[];
  total: number;
}

@Injectable({ providedIn: 'root' })
export class ApiService {

  private readonly base = environment.coordinatorUrl;

  constructor(private http: HttpClient) {}

  // ── Nodes ─────────────────────────────���──────────────────────────────────────

  getNodes(): Observable<Node[]> {
    return this.http.get<ApiNodeListResponse>(`${this.base}/nodes`).pipe(
      map(resp => resp.nodes.map(n => ({
        node_id:       n.id,
        address:       n.address,
        healthy:       n.health === 'healthy',
        last_seen:     n.last_seen,
        running_jobs:  n.running_jobs,
        cpu_percent:   n.cpu_percent ?? 0,
        mem_percent:   n.mem_percent ?? 0,
        registered_at: n.registered_at ?? n.last_seen,
      })))
    );
  }

  // ── Jobs ─────────────────────────────────────────────────────────────────────

  getJobs(page = 0, size = 25, status?: string): Observable<JobsPage> {
    let params = new HttpParams()
      .set('page', page + 1)   // API is 1-indexed
      .set('size', size);
    if (status) params = params.set('status', status);
    return this.http.get<JobsPage>(`${this.base}/jobs`, { params });
  }

  getJob(id: string): Observable<Job> {
    return this.http.get<Job>(`${this.base}/jobs/${id}`);
  }

  submitJob(req: SubmitJobRequest): Observable<Job> {
    return this.http.post<Job>(`${this.base}/jobs`, req);
  }

  // ── Metrics ──────────────────────────────────────────────────────────────────

  getMetrics(): Observable<ClusterMetrics> {
    return this.http.get<any>(`${this.base}/metrics`).pipe(
      map(m => mapMetrics(m))
    );
  }

  // ── Audit ────────────────────────────────────────────────────────────────────

  getAudit(page = 0, size = 50, type?: string): Observable<AuditPage> {
    let params = new HttpParams()
      .set('page', page + 1)   // API is 1-indexed
      .set('size', size);
    if (type) params = params.set('type', type);
    return this.http.get<AuditPage>(`${this.base}/audit`, { params });
  }
}

/**
 * Map the coordinator's nested ClusterMetrics response to the flat
 * shape the dashboard components expect.
 *
 * API:       { nodes: { total, healthy }, jobs: { running, pending, ... }, timestamp }
 * Dashboard: { total_nodes, healthy_nodes, running_jobs, pending_jobs, ... , timestamp }
 */
export function mapMetrics(m: any): ClusterMetrics {
  // Already flat (e.g. from a unit-test mock) — pass through
  if (m.total_nodes !== undefined) return m as ClusterMetrics;
  return {
    timestamp:      m.timestamp,
    total_nodes:    m.nodes?.total   ?? 0,
    healthy_nodes:  m.nodes?.healthy ?? 0,
    total_jobs:     m.jobs?.total     ?? 0,
    running_jobs:   m.jobs?.running   ?? 0,
    pending_jobs:   m.jobs?.pending   ?? 0,
    completed_jobs: m.jobs?.completed ?? 0,
    failed_jobs:    m.jobs?.failed    ?? 0,
  };
}
