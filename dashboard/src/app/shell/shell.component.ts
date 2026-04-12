// src/app/shell/shell.component.ts
import { Component } from '@angular/core';
import { Router, RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { CommonModule } from '@angular/common';
import { MatTooltipModule } from '@angular/material/tooltip';
import { AuthService } from '../core/services/auth.service';

interface NavItem {
  path:  string;
  label: string;
  icon:  string;
}

@Component({
  selector: 'app-shell',
  standalone: true,
  imports: [CommonModule, RouterOutlet, RouterLink, RouterLinkActive, MatTooltipModule],
  template: `
<div class="shell">

  <!-- ── Sidebar ── -->
  <nav class="sidebar">
    <div class="sidebar__brand">
      <span class="brand-icon">⬡</span>
      <span class="brand-name">HELION</span>
      <span class="brand-version">v2</span>
    </div>

    <ul class="sidebar__nav">
      <li *ngFor="let item of navItems">
        <a
          class="nav-link"
          [routerLink]="item.path"
          routerLinkActive="nav-link--active"
          [matTooltip]="item.label"
          matTooltipPosition="right"
        >
          <span class="nav-link__icon material-icons">{{ item.icon }}</span>
          <span class="nav-link__label">{{ item.label }}</span>
        </a>
      </li>
    </ul>

    <div class="sidebar__footer">
      <button class="logout-btn" (click)="logout()" matTooltip="Logout" matTooltipPosition="right">
        <span class="material-icons">logout</span>
        <span>LOGOUT</span>
      </button>
    </div>
  </nav>

  <!-- ── Main content ── -->
  <main class="main-content">
    <router-outlet />
  </main>

</div>
  `,
  styles: [`
    .shell {
      display: flex;
      height: 100vh;
      overflow: hidden;
    }

    /* ── Sidebar ── */
    .sidebar {
      width: var(--sidebar-width);
      min-width: var(--sidebar-width);
      background: var(--color-surface);
      border-right: 1px solid var(--color-border);
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }

    .sidebar__brand {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 20px 16px 16px;
      border-bottom: 1px solid var(--color-border);
      margin-bottom: 8px;
    }

    .brand-icon {
      font-size: 22px;
      color: var(--color-accent);
      line-height: 1;
    }

    .brand-name {
      font-family: var(--font-ui);
      font-size: 15px;
      font-weight: 700;
      letter-spacing: 0.12em;
      color: #e8edf2;
    }

    .brand-version {
      font-size: 10px;
      color: var(--color-accent-dim);
      background: var(--color-accent-glow);
      border: 1px solid rgba(192,132,252,0.2);
      border-radius: 2px;
      padding: 1px 5px;
      letter-spacing: 0.06em;
    }

    .sidebar__nav {
      list-style: none;
      margin: 0;
      padding: 0 8px;
      flex: 1;
    }

    .nav-link {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 9px 10px;
      border-radius: var(--radius);
      color: #7888a0;
      text-decoration: none;
      font-size: 12px;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      transition: color 0.15s, background 0.15s;
      margin-bottom: 2px;

      &:hover {
        color: #c8d0dc;
        background: var(--color-surface-2);
      }

      &.nav-link--active {
        color: var(--color-accent) !important;
        background: var(--color-accent-glow) !important;
        border-left: 2px solid var(--color-accent);
      }
    }

    .nav-link__icon {
      font-size: 18px;
    }

    .sidebar__footer {
      padding: 12px 8px;
      border-top: 1px solid var(--color-border);
    }

    .logout-btn {
      display: flex;
      align-items: center;
      gap: 10px;
      width: 100%;
      padding: 9px 10px;
      background: none;
      border: none;
      border-radius: var(--radius);
      color: #4a5568;
      font-family: var(--font-mono);
      font-size: 12px;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      cursor: pointer;
      transition: color 0.15s, background 0.15s;

      .material-icons { font-size: 18px; }

      &:hover {
        color: var(--color-error);
        background: rgba(255,82,82,0.08);
      }
    }

    /* ── Main content ── */
    .main-content {
      flex: 1;
      overflow-y: auto;
      background: var(--color-bg);
    }
  `]
})
export class ShellComponent {
  readonly navItems: NavItem[] = [
    { path: '/nodes',   label: 'Nodes',   icon: 'dns'           },
    { path: '/jobs',      label: 'Jobs',      icon: 'work_outline'  },
    { path: '/workflows', label: 'Workflows', icon: 'account_tree'  },
    { path: '/metrics', label: 'Metrics', icon: 'show_chart'    },
    { path: '/audit',   label: 'Audit',   icon: 'receipt_long'  },
  ];

  constructor(private auth: AuthService, private router: Router) {}

  logout(): void {
    this.auth.logout();
  }
}
