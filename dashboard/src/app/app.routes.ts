// src/app/app.routes.ts
import { Routes } from '@angular/router';
import { authGuard } from './core/guards/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    loadComponent: () =>
      import('./features/auth/login.component').then(m => m.LoginComponent),
  },
  {
    path: '',
    loadComponent: () =>
      import('./shell/shell.component').then(m => m.ShellComponent),
    canActivate: [authGuard],
    children: [
      { path: '', redirectTo: 'nodes', pathMatch: 'full' },
      {
        path: 'nodes',
        loadComponent: () =>
          import('./features/nodes/node-list.component').then(m => m.NodeListComponent),
      },
      {
        path: 'jobs',
        loadComponent: () =>
          import('./features/jobs/job-list.component').then(m => m.JobListComponent),
      },
      {
        path: 'jobs/:id',
        loadComponent: () =>
          import('./features/jobs/job-detail.component').then(m => m.JobDetailComponent),
      },
      {
        path: 'workflows',
        loadComponent: () =>
          import('./features/workflows/workflow-list.component').then(m => m.WorkflowListComponent),
      },
      {
        path: 'workflows/:id',
        loadComponent: () =>
          import('./features/workflows/workflow-detail.component').then(m => m.WorkflowDetailComponent),
      },
      {
        path: 'events',
        loadComponent: () =>
          import('./features/events/event-feed.component').then(m => m.EventFeedComponent),
      },
      {
        path: 'metrics',
        loadComponent: () =>
          import('./features/metrics/cluster-metrics.component').then(m => m.ClusterMetricsComponent),
      },
      {
        path: 'audit',
        loadComponent: () =>
          import('./features/audit/audit-log.component').then(m => m.AuditLogComponent),
      },
      {
        path: 'analytics',
        loadComponent: () =>
          import('./features/analytics/analytics-dashboard.component').then(m => m.AnalyticsDashboardComponent),
      },
      // Feature 18 — ML module. Three lazy-loaded views; `ml` alone
      // redirects to the Datasets view so sidebar clicks land on a
      // meaningful page rather than a blank outlet.
      { path: 'ml', redirectTo: 'ml/datasets', pathMatch: 'full' },
      {
        path: 'ml/datasets',
        loadComponent: () =>
          import('./features/ml/ml-datasets.component').then(m => m.MlDatasetsComponent),
      },
      {
        path: 'ml/models',
        loadComponent: () =>
          import('./features/ml/ml-models.component').then(m => m.MlModelsComponent),
      },
      {
        path: 'ml/services',
        loadComponent: () =>
          import('./features/ml/ml-services.component').then(m => m.MlServicesComponent),
      },
      // Pipelines list + per-workflow DAG detail view. The detail
      // component imports mermaid lazily so the main bundle stays
      // small for users who never open a DAG.
      {
        path: 'ml/pipelines',
        loadComponent: () =>
          import('./features/ml/ml-pipelines.component').then(m => m.MlPipelinesComponent),
      },
      {
        path: 'ml/pipelines/:id',
        loadComponent: () =>
          import('./features/ml/ml-pipeline-detail.component').then(m => m.MlPipelineDetailComponent),
      },
      // Feature 22 — submission tab. Parent loads the shell (tab
      // bar + outlet), children render the per-tab form. Step 1
      // ships placeholders under each tab; later steps replace
      // them with the real forms without touching routing.
      {
        path: 'submit',
        loadComponent: () =>
          import('./features/submit/submit-shell.component').then(m => m.SubmitShellComponent),
        children: [
          { path: '', redirectTo: 'job', pathMatch: 'full' },
          {
            path: 'job',
            loadComponent: () =>
              import('./features/submit/submit-job.component').then(m => m.SubmitJobComponent),
          },
          {
            path: 'workflow',
            data: { tab: 'workflow' },
            loadComponent: () =>
              import('./features/submit/submit-placeholder.component').then(m => m.SubmitPlaceholderComponent),
          },
          {
            path: 'ml-workflow',
            data: { tab: 'ml-workflow' },
            loadComponent: () =>
              import('./features/submit/submit-placeholder.component').then(m => m.SubmitPlaceholderComponent),
          },
        ],
      },
    ],
  },
  { path: '**', redirectTo: '' },
];
