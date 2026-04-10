// src/app/features/metrics/cluster-metrics.component.spec.ts
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';
import { Subject } from 'rxjs';

import { ClusterMetricsComponent } from './cluster-metrics.component';
import { WebSocketService } from '../../core/services/websocket.service';
import { ClusterMetrics } from '../../shared/models';

const makeSnapshot = (overrides: Partial<ClusterMetrics> = {}): ClusterMetrics => ({
  timestamp:      new Date().toISOString(),
  total_nodes:    4,
  healthy_nodes:  3,
  total_jobs:     20,
  running_jobs:   2,
  pending_jobs:   1,
  completed_jobs: 16,
  failed_jobs:    1,
  ...overrides,
});

describe('ClusterMetricsComponent', () => {
  let fixture:    ComponentFixture<ClusterMetricsComponent>;
  let component:  ClusterMetricsComponent;
  let wsSpy:      jasmine.SpyObj<WebSocketService>;
  let metrics$:   Subject<ClusterMetrics>;

  beforeEach(async () => {
    metrics$ = new Subject<ClusterMetrics>();
    wsSpy = jasmine.createSpyObj('WebSocketService', ['metrics', 'jobLogs']);
    wsSpy.metrics.and.returnValue(metrics$.asObservable());

    await TestBed.configureTestingModule({
      imports: [ClusterMetricsComponent],
      providers: [
        provideAnimations(),
        { provide: WebSocketService, useValue: wsSpy },
      ],
    }).compileComponents();

    fixture   = TestBed.createComponent(ClusterMetricsComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => {
    metrics$.complete();
    fixture.destroy();
  });

  it('should create', () => expect(component).toBeTruthy());

  it('should subscribe to metrics WebSocket on init', () => {
    expect(wsSpy.metrics).toHaveBeenCalled();
  });

  it('should start with no latest snapshot (shows waiting state)', () => {
    expect(component.latest).toBeNull();
    expect(component.connected).toBeFalse();
  });

  it('should update latest and set connected = true on first snapshot', () => {
    const snap = makeSnapshot();
    metrics$.next(snap);
    expect(component.latest).toEqual(snap);
    expect(component.connected).toBeTrue();
  });

  it('should accumulate chart data points', () => {
    metrics$.next(makeSnapshot({ healthy_nodes: 3, running_jobs: 1 }));
    metrics$.next(makeSnapshot({ healthy_nodes: 2, running_jobs: 3 }));
    expect(component.healthyData).toEqual([3, 2]);
    expect(component.runningData).toEqual([1, 3]);
    expect(component.labels.length).toBe(2);
  });

  it('should cap chart data at MAX_POINTS (60)', () => {
    for (let i = 0; i < 65; i++) {
      metrics$.next(makeSnapshot());
    }
    expect(component.labels.length).toBe(60);
    expect(component.healthyData.length).toBe(60);
    expect(component.runningData.length).toBe(60);
  });

  it('nodeHealthPct: returns correct percentage string', () => {
    metrics$.next(makeSnapshot({ total_nodes: 4, healthy_nodes: 3 }));
    expect(component.nodeHealthPct).toBe('75');
  });

  it('nodeHealthPct: returns "0" when no nodes', () => {
    metrics$.next(makeSnapshot({ total_nodes: 0, healthy_nodes: 0 }));
    expect(component.nodeHealthPct).toBe('0');
  });

  it('nodeHealthPct: returns "0" before first snapshot', () => {
    expect(component.nodeHealthPct).toBe('0');
  });

  it('should show error and set connected = false on WS error', () => {
    metrics$.error(new Error('connection refused'));
    expect(component.connected).toBeFalse();
    expect(component.error).toContain('connection refused');
  });

  it('should set connected = false on WS complete', () => {
    metrics$.next(makeSnapshot());
    metrics$.complete();
    expect(component.connected).toBeFalse();
  });

  it('should unsubscribe on destroy', () => {
    metrics$.next(makeSnapshot());
    fixture.destroy();
    // Should not throw after destroy
    expect(() => metrics$.next(makeSnapshot())).not.toThrow();
  });
});