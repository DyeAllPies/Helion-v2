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

  onopen:    (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror:   (() => void) | null = null;
  onclose:   ((e: { code: number; reason: string }) => void) | null = null;

  lastSent = '';

  constructor(public url: string) { MockWebSocket.instance = this; }

  send(data: string) { this.lastSent = data; }

  close(code = 1000, reason = '') {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  // Test helpers
  simulateOpen()                 { this.onopen?.(); }
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

  // AUDIT 2026-04-12-01/H2: token is no longer in the URL — sent as first-message frame.
  it('jobLogs should connect without token in URL and send auth frame', (done) => {
    service.jobLogs('job-001').subscribe({ complete: done });
    expect(MockWebSocket.instance.url).toContain('/ws/jobs/job-001/logs');
    expect(MockWebSocket.instance.url).not.toContain('token=');
    // Trigger onopen to send the auth frame
    MockWebSocket.instance.simulateOpen();
    expect(MockWebSocket.instance.lastSent).toContain('"type":"auth"');
    expect(MockWebSocket.instance.lastSent).toContain('test-jwt');
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

  it('should error observable when onerror fires', (done) => {
    service.jobLogs('job-002').subscribe({
      error: err => {
        expect(err.message).toContain('WebSocket error');
        done();
      },
    });
    MockWebSocket.instance.simulateError();
  });

  it('should omit token param when auth token is null', (done) => {
    (Object.getOwnPropertyDescriptor(authSpy, 'token')!.get as jasmine.Spy).and.returnValue(null);
    service.jobLogs('job-003').subscribe({ complete: done });
    expect(MockWebSocket.instance.url).not.toContain('token=');
    MockWebSocket.instance.simulateClose(1000);
  });

  it('should skip malformed JSON frames without erroring', (done) => {
    const received: LogChunk[] = [];
    service.jobLogs('job-004').subscribe({
      next: c => received.push(c),
      complete: () => {
        expect(received.length).toBe(0);
        done();
      },
    });
    MockWebSocket.instance.onmessage?.({ data: 'not-json{{{' });
    MockWebSocket.instance.simulateClose(1000);
  });
});
