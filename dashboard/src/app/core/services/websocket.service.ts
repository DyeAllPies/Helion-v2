// src/app/core/services/websocket.service.ts
//
// Creates typed WebSocket Observables for the two coordinator WS endpoints:
//   GET /ws/jobs/{id}/logs  — streams LogChunk frames
//   GET /ws/metrics         — streams ClusterMetrics snapshots every 5 s
//
// Each call returns a cold Observable that:
//   - opens the connection on subscribe
//   - closes it on unsubscribe / complete / error
//   - attaches the JWT as a query param (WS headers not supported in browser)
//   - emits parsed JSON frames of type T

import { Injectable } from '@angular/core';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';
import { environment } from '../../../environments/environment';
import { AuthService } from './auth.service';
import { mapMetrics } from './api.service';
import { LogChunk, MetricsFrame } from '../../shared/models';

@Injectable({ providedIn: 'root' })
export class WebSocketService {

  constructor(private auth: AuthService) {}

  // ── Public connections ────────────────────────────────────────────────────────

  jobLogs(jobId: string): Observable<LogChunk> {
    const url = `${environment.wsUrl}/ws/jobs/${jobId}/logs`;
    return this._connect<LogChunk>(url);
  }

  metrics(): Observable<MetricsFrame> {
    const url = `${environment.wsUrl}/ws/metrics`;
    return this._connect<any>(url).pipe(map(m => mapMetrics(m)));
  }

  // ── Private factory ───────────────────────────────────────────────────────────

  // AUDIT 2026-04-12/H2 (fixed): token is sent as the first WebSocket message
  // instead of a URL query parameter. This keeps the JWT out of server access
  // logs, browser history, and Referer headers.
  private _connect<T>(baseUrl: string): Observable<T> {
    return new Observable<T>(observer => {
      // Connect without token in URL — auth happens via first-message frame.
      let ws: WebSocket | null = new WebSocket(baseUrl);

      ws.onopen = () => {
        const token = this.auth.token;
        if (token && ws) {
          ws.send(JSON.stringify({ type: 'auth', token }));
        }
      };

      ws.onmessage = ({ data }) => {
        try {
          const msg = JSON.parse(data as string);
          // Server sends {"type":"auth_ok"} after successful auth — skip it.
          if (msg.type === 'auth_ok' || msg.type === 'auth_error') {
            if (msg.type === 'auth_error') {
              observer.error(new Error('WebSocket authentication failed'));
            }
            return;
          }
          observer.next(msg as T);
        } catch {
          // malformed frame — skip
        }
      };

      ws.onerror = () => {
        observer.error(new Error(`WebSocket error: ${baseUrl}`));
      };

      ws.onclose = ({ code, reason }) => {
        if (code === 1000) {
          observer.complete();
        } else {
          observer.error(new Error(`WebSocket closed ${code}: ${reason}`));
        }
      };

      // Teardown — called on unsubscribe
      return () => {
        if (ws && ws.readyState !== WebSocket.CLOSED) {
          ws.close(1000, 'unsubscribed');
        }
        ws = null;
      };
    });
  }
}
