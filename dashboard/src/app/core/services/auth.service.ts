// src/app/core/services/auth.service.ts
//
// AuthService owns the JWT lifecycle.
//
// Security contract (from design doc §9.3):
//   • Token stored ONLY in a private BehaviorSubject (memory).
//   • Never written to localStorage, sessionStorage, or cookies.
//   • Token is lost on page refresh — user must re-enter it.
//   • Auto-logout 30 s before JWT expiry.
//   • HTTP interceptor calls token$ to attach Bearer header.
//   • 401 from any endpoint triggers clearToken() + redirect to /login.

import { Injectable, OnDestroy } from '@angular/core';
import { Router } from '@angular/router';
import { BehaviorSubject, Observable, Subscription, timer } from 'rxjs';
import { map } from 'rxjs/operators';
import { environment } from '../../../environments/environment';

@Injectable({ providedIn: 'root' })
export class AuthService implements OnDestroy {

  /** Emits the raw JWT string or null when logged out. */
  private readonly _token$ = new BehaviorSubject<string | null>(null);

  /** Emits true while a valid token is held in memory. */
  readonly isAuthenticated$: Observable<boolean> = this._token$.pipe(
    map(t => t !== null)
  );

  /**
   * Feature 32 — emits the JWT `role` claim (or null when no
   * token is held). Components that gate UI on admin role
   * (e.g. the operator-cert issuance panel) read from here
   * instead of calling the endpoint and checking for 403.
   *
   * The JWT payload is parsed without signature verification
   * — the browser is consuming claims the SERVER stamped and
   * signed; an attacker who can forge a JWT already bypassed
   * everything else. Role shown in the UI is purely for
   * guarding render + navigation; every privileged endpoint
   * is still server-gated (feature 37 authz).
   */
  readonly userRole$: Observable<string | null> = this._token$.pipe(
    map(t => (t ? this._decodePayload(t)?.role ?? null : null))
  );

  /** Emits true when the held JWT carries role=admin. */
  readonly isAdmin$: Observable<boolean> = this.userRole$.pipe(
    map(r => r === 'admin')
  );

  private _expiryTimer?: Subscription;

  constructor(private router: Router) {}

  // ── Public API ───────────────────────────────────────────────────────────────

  /** Returns the current JWT or null synchronously (for the interceptor). */
  get token(): string | null {
    return this._token$.value;
  }

  /**
   * Validate and store the given JWT.
   * Returns false (and does NOT store) if the token is expired or malformed.
   */
  login(rawToken: string): boolean {
    const payload = this._decodePayload(rawToken);
    if (!payload) return false;

    const expiresAt = payload.exp * 1000; // convert to ms
    if (Date.now() >= expiresAt) return false;

    this._token$.next(rawToken);
    this._scheduleAutoLogout(expiresAt);
    return true;
  }

  /** Clear the in-memory token and navigate to login. */
  logout(): void {
    this._token$.next(null);
    this._cancelAutoLogout();
    void this.router.navigate(['/login']);
  }

  /** Called by the HTTP interceptor when any response returns 401. */
  onUnauthorized(): void {
    this.logout();
  }

  // ── Private helpers ───────────────────────────────────────────────────────────

  /**
   * Decode a JWT payload without verifying the signature
   * (browser-only read). The `role` claim is optional — some
   * feature-19 scoped tokens carry `role: "job"`, others
   * `role: "admin"`, and older tokens minted before feature
   * 27 may omit it entirely. Callers treat an absent role
   * as "non-admin" (safe default).
   */
  private _decodePayload(jwt: string): { exp: number; role?: string } | null {
    try {
      const parts = jwt.split('.');
      if (parts.length !== 3) return null;
      const padded = parts[1].replace(/-/g, '+').replace(/_/g, '/');
      const json = atob(padded.padEnd(padded.length + (4 - padded.length % 4) % 4, '='));
      const parsed = JSON.parse(json);
      if (typeof parsed !== 'object' || parsed === null) return null;
      const exp = (parsed as { exp?: unknown }).exp;
      if (typeof exp !== 'number') return null;
      const role = (parsed as { role?: unknown }).role;
      return {
        exp,
        role: typeof role === 'string' ? role : undefined,
      };
    } catch {
      return null;
    }
  }

  /**
   * Schedule automatic logout before the token expires.
   * Uses (expiresAt - now - jwtExpiryBufferMs) as the delay.
   */
  private _scheduleAutoLogout(expiresAtMs: number): void {
    this._cancelAutoLogout();
    const delay = expiresAtMs - Date.now() - environment.jwtExpiryBufferMs;
    if (delay <= 0) {
      this.logout();
      return;
    }
    this._expiryTimer = timer(delay).subscribe(() => this.logout());
  }

  private _cancelAutoLogout(): void {
    this._expiryTimer?.unsubscribe();
    this._expiryTimer = undefined;
  }

  ngOnDestroy(): void {
    this._cancelAutoLogout();
  }
}
