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
  Job, JobsPage, JobLogsResponse, Node, ClusterMetrics, AuditPage, SubmitJobRequest,
  Workflow, WorkflowsPage, SubmitWorkflowRequest,
  AnalyticsThroughputResponse, AnalyticsNodeReliabilityRow, AnalyticsRetryRow,
  AnalyticsQueueWaitResponse, AnalyticsWorkflowOutcomesResponse,
  Dataset, DatasetListResponse, DatasetRegisterRequest,
  MLModel, ModelListResponse, ModelRegisterRequest,
  ServiceEndpoint, ServiceListResponse,
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
  cpu_millicores?: number;
  total_mem_bytes?: number;
  max_slots?: number;
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
        node_id:         n.id,
        address:         n.address,
        healthy:         n.health === 'healthy',
        last_seen:       n.last_seen,
        running_jobs:    n.running_jobs,
        cpu_percent:     n.cpu_percent ?? 0,
        mem_percent:     n.mem_percent ?? 0,
        registered_at:   n.registered_at ?? n.last_seen,
        cpu_millicores:  n.cpu_millicores,
        total_mem_bytes: n.total_mem_bytes,
        max_slots:       n.max_slots,
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

  getJobLogs(id: string, tail?: number): Observable<JobLogsResponse> {
    let params = new HttpParams();
    if (tail) params = params.set('tail', tail);
    return this.http.get<JobLogsResponse>(`${this.base}/jobs/${id}/logs`, { params });
  }

  // ── Metrics ──────────────────────────────────────────────────────────────────

  getMetrics(): Observable<ClusterMetrics> {
    return this.http.get<any>(`${this.base}/metrics`).pipe(
      map(m => mapMetrics(m))
    );
  }

  // ── Workflows ───────────────────────────────────────────────────────────────

  getWorkflows(page = 0, size = 20): Observable<WorkflowsPage> {
    const params = new HttpParams()
      .set('page', page + 1)
      .set('size', size);
    return this.http.get<WorkflowsPage>(`${this.base}/workflows`, { params });
  }

  getWorkflow(id: string): Observable<Workflow> {
    return this.http.get<Workflow>(`${this.base}/workflows/${id}`);
  }

  submitWorkflow(req: SubmitWorkflowRequest): Observable<Workflow> {
    return this.http.post<Workflow>(`${this.base}/workflows`, req);
  }

  cancelWorkflow(id: string): Observable<Record<string, unknown>> {
    return this.http.delete<Record<string, unknown>>(`${this.base}/workflows/${id}`);
  }

  // ── Audit ────────────────────────────────────────────────────────────────────

  getAudit(page = 0, size = 50, type?: string): Observable<AuditPage> {
    let params = new HttpParams()
      .set('page', page + 1)   // API is 1-indexed
      .set('size', size);
    if (type) params = params.set('type', type);
    return this.http.get<AuditPage>(`${this.base}/audit`, { params });
  }

  // ── Analytics ───────────────────────────────────────────────────────────────

  getAnalyticsThroughput(from: string, to: string): Observable<AnalyticsThroughputResponse> {
    const params = new HttpParams().set('from', from).set('to', to);
    return this.http.get<AnalyticsThroughputResponse>(
      `${this.base}/api/analytics/throughput`, { params });
  }

  getAnalyticsNodeReliability(): Observable<{ data: AnalyticsNodeReliabilityRow[] }> {
    return this.http.get<{ data: AnalyticsNodeReliabilityRow[] }>(
      `${this.base}/api/analytics/node-reliability`);
  }

  getAnalyticsRetryEffectiveness(): Observable<{ data: AnalyticsRetryRow[] }> {
    return this.http.get<{ data: AnalyticsRetryRow[] }>(
      `${this.base}/api/analytics/retry-effectiveness`);
  }

  getAnalyticsQueueWait(from: string, to: string): Observable<AnalyticsQueueWaitResponse> {
    const params = new HttpParams().set('from', from).set('to', to);
    return this.http.get<AnalyticsQueueWaitResponse>(
      `${this.base}/api/analytics/queue-wait`, { params });
  }

  getAnalyticsWorkflowOutcomes(from: string, to: string): Observable<AnalyticsWorkflowOutcomesResponse> {
    const params = new HttpParams().set('from', from).set('to', to);
    return this.http.get<AnalyticsWorkflowOutcomesResponse>(
      `${this.base}/api/analytics/workflow-outcomes`, { params });
  }

  // ── ML registry: datasets ───────────────────────────────────────────────────

  getDatasets(page = 0, size = 25): Observable<DatasetListResponse> {
    const params = new HttpParams().set('page', page + 1).set('size', size);
    return this.http.get<DatasetListResponse>(`${this.base}/api/datasets`, { params });
  }

  getDataset(name: string, version: string): Observable<Dataset> {
    return this.http.get<Dataset>(
      `${this.base}/api/datasets/${encodeURIComponent(name)}/${encodeURIComponent(version)}`);
  }

  registerDataset(req: DatasetRegisterRequest): Observable<Dataset> {
    return this.http.post<Dataset>(`${this.base}/api/datasets`, req);
  }

  deleteDataset(name: string, version: string): Observable<void> {
    return this.http.delete<void>(
      `${this.base}/api/datasets/${encodeURIComponent(name)}/${encodeURIComponent(version)}`);
  }

  // ── ML registry: models ─────────────────────────────────────────────────────

  getModels(page = 0, size = 25): Observable<ModelListResponse> {
    const params = new HttpParams().set('page', page + 1).set('size', size);
    return this.http.get<ModelListResponse>(`${this.base}/api/models`, { params });
  }

  getModel(name: string, version: string): Observable<MLModel> {
    return this.http.get<MLModel>(
      `${this.base}/api/models/${encodeURIComponent(name)}/${encodeURIComponent(version)}`);
  }

  getLatestModel(name: string): Observable<MLModel> {
    return this.http.get<MLModel>(
      `${this.base}/api/models/${encodeURIComponent(name)}/latest`);
  }

  registerModel(req: ModelRegisterRequest): Observable<MLModel> {
    return this.http.post<MLModel>(`${this.base}/api/models`, req);
  }

  deleteModel(name: string, version: string): Observable<void> {
    return this.http.delete<void>(
      `${this.base}/api/models/${encodeURIComponent(name)}/${encodeURIComponent(version)}`);
  }

  // ── ML inference services (feature 17) ──────────────────────────────────────

  getServices(): Observable<ServiceListResponse> {
    return this.http.get<ServiceListResponse>(`${this.base}/api/services`);
  }

  getService(jobId: string): Observable<ServiceEndpoint> {
    return this.http.get<ServiceEndpoint>(
      `${this.base}/api/services/${encodeURIComponent(jobId)}`);
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
