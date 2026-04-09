// src/app/core/interceptors/auth.interceptor.spec.ts
import { TestBed } from '@angular/core/testing';
import {
  HttpClient,
  provideHttpClient, withInterceptors,
} from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Router } from '@angular/router';

import { authInterceptor } from './auth.interceptor';
import { AuthService } from '../services/auth.service';

describe('authInterceptor', () => {
  let http:       HttpClient;
  let httpMock:   HttpTestingController;
  let authSpy:    jasmine.SpyObj<AuthService>;
  let routerSpy:  jasmine.SpyObj<Router>;

  const setupWithToken = (token: string | null) => {
    authSpy   = jasmine.createSpyObj('AuthService', ['onUnauthorized'], { token });
    routerSpy = jasmine.createSpyObj('Router', ['navigate']);
    routerSpy.navigate.and.returnValue(Promise.resolve(true));

    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptors([authInterceptor])),
        provideHttpClientTesting(),
        { provide: AuthService, useValue: authSpy },
        { provide: Router,      useValue: routerSpy },
      ],
    });

    http     = TestBed.inject(HttpClient);
    httpMock = TestBed.inject(HttpTestingController);
  };

  afterEach(() => httpMock.verify());

  it('should attach Bearer token when token is present', () => {
    setupWithToken('my-jwt');
    http.get('/nodes').subscribe();

    const req = httpMock.expectOne('/nodes');
    expect(req.request.headers.get('Authorization')).toBe('Bearer my-jwt');
    req.flush([]);
  });

  it('should NOT attach Authorization header when no token', () => {
    setupWithToken(null);
    http.get('/nodes').subscribe();

    const req = httpMock.expectOne('/nodes');
    expect(req.request.headers.has('Authorization')).toBeFalse();
    req.flush([]);
  });

  it('should call onUnauthorized on 401 response', () => {
    setupWithToken('expired-jwt');
    http.get('/nodes').subscribe({ error: () => {} });

    const req = httpMock.expectOne('/nodes');
    req.flush({ error: 'Unauthorized' }, { status: 401, statusText: 'Unauthorized' });

    expect(authSpy.onUnauthorized).toHaveBeenCalled();
  });

  it('should NOT call onUnauthorized on non-401 errors', () => {
    setupWithToken('my-jwt');
    http.get('/nodes').subscribe({ error: () => {} });

    const req = httpMock.expectOne('/nodes');
    req.flush({ error: 'Not Found' }, { status: 404, statusText: 'Not Found' });

    expect(authSpy.onUnauthorized).not.toHaveBeenCalled();
  });
});
