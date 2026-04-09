// src/app/core/services/api.service.ts
//
// Thin wrapper around Angular HttpClient.
// All requests go to environment.coordinatorUrl.
// In production Nginx proxies /api and /ws on the same origin, so coordinatorUrl is ''.

import { Injectable } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';
import { environment } from '../../../environments/environment';
import {
  Job, JobsPage, Node, ClusterMetrics, AuditPage, SubmitJobRequest
} from '../../shared/models';

@Injectable({ providedIn: 'root' })
export class ApiService {

  private readonly base = environment.coordinatorUrl;

  constructor(private http: HttpClient) {}

  // ── Nodes ────────────────────────────────────────────────────────────────────

  getNodes(): Observable<Node[]> {
    return this.http.get<Node[]>(`${this.base}/nodes`);
  }

  // ── Jobs ─────────────────────────────────────────────────────────────────────

  getJobs(page = 0, size = 25, status?: string): Observable<JobsPage> {
    let params = new HttpParams()
      .set('page', page)
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
    return this.http.get<ClusterMetrics>(`${this.base}/metrics`);
  }

  // ── Audit ────────────────────────────────────────────────────────────────────

  getAudit(page = 0, size = 50, type?: string): Observable<AuditPage> {
    let params = new HttpParams()
      .set('page', page)
      .set('size', size);
    if (type) params = params.set('type', type);
    return this.http.get<AuditPage>(`${this.base}/audit`, { params });
  }
}
