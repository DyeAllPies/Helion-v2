// src/app/core/services/websocket.service.spec.ts
import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';

import { WebSocketService } from './websocket.service';
import { AuthService } from './auth.service';
import { LogChunk } from '../../shared/models';

// Minimal WebSocket mock
class MockWebSocket {
  static instance: MockWebSocket;
  static CLOSED = 3;
  readyState = 1; // OPEN

  onmessage: ((e: { data: string }) => void) | null = null;
  onerror:   (() => void) | null = null;
  onclose:   ((e: { code: number; reason: string }) => void) | null = null;

  constructor(public url: string) { MockWebSocket.instance = this; }

  close(code = 1000, reason = '') {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  // Test helpers
  simulateMessage(data: unknown) { this.onmessage?.({ data: JSON.stringify(data) }); }
  simulateError()                { this.onerror?.(); }
  simulateClose(code = 1000)     { this.close(code); }
}

describe('WebSocketService', () => {
  let service: WebSocketService;
  let authSpy: jasmine.SpyObj<AuthService>;

  beforeEach(() => {
    (window as unknown as { WebSocket: unknown }).WebSocket = MockWebSocket;

    authSpy = jasmine.createSpyObj('AuthService', ['logout', 'onUnauthorized'], { token: 'test-jwt' });

    TestBed.configureTestingModule({
      providers: [
        WebSocketService,
        { provide: AuthService, useValue: authSpy },
        { provide: Router, useValue: {} },
      ],
    });

    service = TestBed.inject(WebSocketService);
  });

  it('should be created', () => expect(service).toBeTruthy());

  it('jobLogs should append token to URL', (done) => {
    service.jobLogs('job-001').subscribe({ complete: done });
    expect(MockWebSocket.instance.url).toContain('token=test-jwt');
    expect(MockWebSocket.instance.url).toContain('/ws/jobs/job-001/logs');
    MockWebSocket.instance.simulateClose(1000);
  });

  it('should emit parsed LogChunk frames', (done) => {
    const received: LogChunk[] = [];
    const chunk: LogChunk = {
      job_id: 'job-001', sequence: 1,
      text: 'hello world', timestamp: new Date().toISOString(),
    };

    service.jobLogs('job-001').subscribe({
      next:     c  => received.push(c),
      complete: () => { expect(received[0]).toEqual(chunk); done(); },
    });

    MockWebSocket.instance.simulateMessage(chunk);
    MockWebSocket.instance.simulateClose(1000);
  });

  it('should complete observable on clean close (1000)', (done) => {
    service.jobLogs('job-001').subscribe({ complete: done });
    MockWebSocket.instance.simulateClose(1000);
  });

  it('should error observable on abnormal close', (done) => {
    service.jobLogs('job-001').subscribe({
      error: err => { expect(err).toBeTruthy(); done(); },
    });
    MockWebSocket.instance.simulateClose(1006);
  });

  it('should close WebSocket on unsubscribe', () => {
    const sub = service.metrics().subscribe();
    sub.unsubscribe();
    expect(MockWebSocket.instance.readyState).toBe(MockWebSocket.CLOSED);
  });
});
