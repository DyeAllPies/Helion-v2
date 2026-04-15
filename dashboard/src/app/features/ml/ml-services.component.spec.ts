// src/app/features/ml/ml-services.component.spec.ts
//
// Services view polls every 5 s via rxjs `interval`; tests use
// fakeAsync + tick to drive the poll without sleeping, and assert
// the subscription is torn down on destroy so the dashboard does
// not leak timers.

import { ComponentFixture, TestBed, fakeAsync, tick, discardPeriodicTasks } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';

import { MlServicesComponent } from './ml-services.component';
import { ApiService } from '../../core/services/api.service';
import { ServiceEndpoint, ServiceListResponse } from '../../shared/models';

function makeEndpoint(over: Partial<ServiceEndpoint> = {}): ServiceEndpoint {
  return {
    job_id: 'svc-1',
    node_id: 'n1',
    node_address: '10.0.0.1:9090',
    port: 8080,
    health_path: '/healthz',
    ready: true,
    upstream_url: 'http://10.0.0.1:8080/healthz',
    updated_at: '2026-04-14T10:00:00Z',
    ...over,
  };
}

function list(services: ServiceEndpoint[]): ServiceListResponse {
  return { services, total: services.length };
}

describe('MlServicesComponent', () => {
  let fixture: ComponentFixture<MlServicesComponent>;
  let component: MlServicesComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['getServices']);
    apiSpy.getServices.and.returnValue(of(list([])));

    await TestBed.configureTestingModule({
      imports: [MlServicesComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(MlServicesComponent);
    component = fixture.componentInstance;
  });

  afterEach(() => fixture.destroy());

  it('creates', fakeAsync(() => {
    fixture.detectChanges();
    expect(component).toBeTruthy();
    discardPeriodicTasks();
  }));

  it('fetches immediately on init (startWith(0))', fakeAsync(() => {
    apiSpy.getServices.and.returnValue(of(list([makeEndpoint()])));
    fixture.detectChanges();
    expect(apiSpy.getServices).toHaveBeenCalledTimes(1);
    expect(component.services.length).toBe(1);
    discardPeriodicTasks();
  }));

  it('polls every 5 seconds', fakeAsync(() => {
    fixture.detectChanges();            // immediate emit
    expect(apiSpy.getServices).toHaveBeenCalledTimes(1);
    tick(5000);
    expect(apiSpy.getServices).toHaveBeenCalledTimes(2);
    tick(5000);
    expect(apiSpy.getServices).toHaveBeenCalledTimes(3);
    discardPeriodicTasks();
  }));

  it('surfaces API errors but keeps polling', fakeAsync(() => {
    apiSpy.getServices.and.returnValue(throwError(() => ({
      error: { error: 'service registry not configured' },
    })));
    fixture.detectChanges();
    expect(component.error).toBe('service registry not configured');
    // catchError swallows so the interval keeps firing.
    tick(5000);
    expect(apiSpy.getServices).toHaveBeenCalledTimes(2);
    discardPeriodicTasks();
  }));

  it('clears error once a successful fetch returns endpoints', fakeAsync(() => {
    let call = 0;
    apiSpy.getServices.and.callFake(() => {
      call++;
      if (call === 1) return throwError(() => ({ error: { error: 'boom' } }));
      return of(list([makeEndpoint()]));
    });
    fixture.detectChanges();
    expect(component.error).toBe('boom');
    tick(5000);
    expect(component.services.length).toBe(1);
    expect(component.error).toBe('');
    discardPeriodicTasks();
  }));

  it('reload() refetches once without polling', () => {
    apiSpy.getServices.calls.reset();
    apiSpy.getServices.and.returnValue(of(list([makeEndpoint()])));
    component.reload();
    expect(apiSpy.getServices).toHaveBeenCalledTimes(1);
    expect(component.services.length).toBe(1);
  });

  it('unsubscribes on destroy', fakeAsync(() => {
    fixture.detectChanges();
    expect(apiSpy.getServices).toHaveBeenCalledTimes(1);
    fixture.destroy();
    tick(5000);
    // No further calls after destroy.
    expect(apiSpy.getServices).toHaveBeenCalledTimes(1);
  }));
});
