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
        path: 'metrics',
        loadComponent: () =>
          import('./features/metrics/cluster-metrics.component').then(m => m.ClusterMetricsComponent),
      },
      {
        path: 'audit',
        loadComponent: () =>
          import('./features/audit/audit-log.component').then(m => m.AuditLogComponent),
      },
    ],
  },
  { path: '**', redirectTo: '' },
];
