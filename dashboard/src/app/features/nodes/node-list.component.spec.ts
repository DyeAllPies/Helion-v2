// src/app/features/nodes/node-list.component.spec.ts
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';

import { NodeListComponent } from './node-list.component';
import { ApiService } from '../../core/services/api.service';
import { Node } from '../../shared/models';

const mockNodes: Node[] = [
  {
    node_id: 'node-abc123', address: '10.0.0.1:9090',
    healthy: true, last_seen: new Date().toISOString(),
    running_jobs: 2, cpu_percent: 34.5, mem_percent: 61.2,
    registered_at: new Date().toISOString(),
  },
  {
    node_id: 'node-def456', address: '10.0.0.2:9090',
    healthy: false, last_seen: new Date(Date.now() - 60_000).toISOString(),
    running_jobs: 0, cpu_percent: 0, mem_percent: 12.0,
    registered_at: new Date().toISOString(),
  },
];

describe('NodeListComponent', () => {
  let fixture: ComponentFixture<NodeListComponent>;
  let component: NodeListComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj('ApiService', ['getNodes']);
    apiSpy.getNodes.and.returnValue(of(mockNodes));

    await TestBed.configureTestingModule({
      imports: [NodeListComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture   = TestBed.createComponent(NodeListComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('should create', () => expect(component).toBeTruthy());

  it('should display 2 nodes after data loads', () => {
    expect(component.nodes.length).toBe(2);
  });

  it('should compute healthyCount correctly', () => {
    expect(component.healthyCount).toBe(1);
  });

  it('should render healthy badge for node-abc123', () => {
    const el: HTMLElement = fixture.nativeElement;
    const badges = el.querySelectorAll('.badge-healthy');
    expect(badges.length).toBeGreaterThanOrEqual(1);
  });

  it('should render unhealthy badge for node-def456', () => {
    const el: HTMLElement = fixture.nativeElement;
    const badges = el.querySelectorAll('.badge-unhealthy');
    expect(badges.length).toBeGreaterThanOrEqual(1);
  });

  it('should show error banner on API failure', () => {
    apiSpy.getNodes.and.returnValue(throwError(() => new Error('Network error')));
    component.ngOnInit();
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.error-banner')).toBeTruthy();
  });

  it('should call getNodes on init', () => {
    // startWith(0) fires immediately — verifies polling starts on component init
    expect(apiSpy.getNodes.calls.count()).toBeGreaterThanOrEqual(1);
  });

  it('relativeTime should return "just now" for very recent timestamps', () => {
    const ts = new Date().toISOString();
    expect(component.relativeTime(ts)).toBe('just now');
  });

  it('relativeTime should return seconds ago', () => {
    const ts = new Date(Date.now() - 30_000).toISOString();
    expect(component.relativeTime(ts)).toBe('30s ago');
  });
});
