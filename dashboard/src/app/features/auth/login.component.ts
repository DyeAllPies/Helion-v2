// src/app/features/auth/login.component.ts
//
// Login page: user pastes the root JWT printed by the coordinator on first start.
// Token is validated client-side (expiry check), then stored in AuthService memory.
// On success → redirect to /nodes.

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatButtonModule } from '@angular/material/button';
import { AuthService } from '../../core/services/auth.service';

@Component({
  selector: 'app-login',
  standalone: true,
  imports: [CommonModule, FormsModule, MatFormFieldModule, MatInputModule, MatButtonModule],
  template: `
<div class="login-page">

  <div class="login-card">
    <!-- ASCII art brand mark -->
    <pre class="ascii-brand">
  ╦ ╦╔═╗╦  ╦╔═╗╔╗╔
  ╠═╣║╣ ║  ║║ ║║║║
  ╩ ╩╚═╝╩═╝╩╚═╝╝╚╝
    </pre>
    <p class="login-subtitle">DISTRIBUTED JOB SCHEDULER — CONTROL PLANE</p>

    <div class="login-form">
      <label class="field-label">BEARER TOKEN</label>
      <textarea
        class="token-input"
        [(ngModel)]="tokenInput"
        placeholder="Paste the root JWT from coordinator stdout..."
        rows="5"
        spellcheck="false"
        autocomplete="off"
        autocorrect="off"
        (keydown.enter)="$event.preventDefault()"
      ></textarea>

      <div class="error-msg" *ngIf="errorMsg">
        <span class="material-icons" style="font-size:14px;vertical-align:middle">error_outline</span>
        {{ errorMsg }}
      </div>

      <button
        class="login-btn"
        [disabled]="!tokenInput.trim() || loading"
        (click)="submit()"
      >
        <span *ngIf="!loading">AUTHENTICATE →</span>
        <span *ngIf="loading">VERIFYING...</span>
      </button>

      <p class="hint">
        Token is stored in memory only and lost on page refresh.<br>
        The root token is printed to coordinator stdout on first start.
      </p>
    </div>
  </div>

</div>
  `,
  styles: [`
    .login-page {
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      background: var(--color-bg);
      background-image:
        radial-gradient(ellipse 60% 40% at 50% 0%, rgba(192,132,252,0.04) 0%, transparent 70%),
        repeating-linear-gradient(90deg, transparent, transparent 39px, rgba(192,132,252,0.03) 39px, rgba(192,132,252,0.03) 40px),
        repeating-linear-gradient(0deg, transparent, transparent 39px, rgba(192,132,252,0.03) 39px, rgba(192,132,252,0.03) 40px);
      padding: 24px;
    }

    .login-card {
      width: 100%;
      max-width: 440px;
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-top: 2px solid var(--color-accent);
      border-radius: var(--radius);
      padding: 36px 32px 28px;
      box-shadow: 0 24px 64px rgba(0,0,0,0.5), 0 0 40px rgba(192,132,252,0.04);
    }

    .ascii-brand {
      font-family: var(--font-mono);
      font-size: 13px;
      color: var(--color-accent);
      margin: 0 0 4px;
      line-height: 1.3;
      text-shadow: 0 0 12px rgba(192,132,252,0.4);
    }

    .login-subtitle {
      font-size: 10px;
      letter-spacing: 0.12em;
      color: var(--color-muted);
      margin: 0 0 28px;
    }

    .login-form {
      display: flex;
      flex-direction: column;
      gap: 12px;
    }

    .field-label {
      font-size: 10px;
      letter-spacing: 0.1em;
      color: var(--color-accent);
      display: block;
      margin-bottom: -4px;
    }

    .token-input {
      width: 100%;
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #c8d0dc;
      font-family: var(--font-mono);
      font-size: 11px;
      padding: 10px 12px;
      resize: vertical;
      outline: none;
      transition: border-color 0.15s;
      line-height: 1.5;

      &::placeholder { color: #3a4555; }
      &:focus { border-color: var(--color-accent-dim); }
    }

    .error-msg {
      font-size: 11px;
      color: var(--color-error);
      display: flex;
      align-items: center;
      gap: 6px;
    }

    .login-btn {
      background: var(--color-accent);
      color: #000;
      border: none;
      border-radius: var(--radius-sm);
      font-family: var(--font-ui);
      font-size: 12px;
      font-weight: 700;
      letter-spacing: 0.1em;
      padding: 11px;
      cursor: pointer;
      transition: background 0.15s, box-shadow 0.15s;

      &:hover:not(:disabled) {
        background: #e9d5ff;
        box-shadow: 0 0 16px rgba(192,132,252,0.3);
      }

      &:disabled {
        opacity: 0.35;
        cursor: not-allowed;
      }
    }

    .hint {
      font-size: 10px;
      color: var(--color-muted);
      line-height: 1.6;
      margin: 0;
      text-align: center;
    }
  `]
})
export class LoginComponent implements OnInit {
  tokenInput = '';
  errorMsg   = '';
  loading    = false;

  constructor(private auth: AuthService, private router: Router) {}

  ngOnInit(): void {
    // Already authenticated → skip login
    if (this.auth.token) {
      void this.router.navigate(['/nodes']);
    }
  }

  submit(): void {
    this.errorMsg = '';
    this.loading  = true;
    const raw     = this.tokenInput.trim();

    // Small tick to show loading state
    setTimeout(() => {
      const ok = this.auth.login(raw);
      this.loading = false;

      if (ok) {
        void this.router.navigate(['/nodes']);
      } else {
        this.errorMsg = 'Token is invalid or expired.';
      }
    }, 200);
  }
}
