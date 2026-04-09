// src/app/core/services/auth.service.spec.ts
import { TestBed, fakeAsync, tick } from '@angular/core/testing';
import { Router } from '@angular/router';
import { AuthService } from './auth.service';

/** Build a JWT with a given expiry (unix seconds). Signature is fake — browser doesn't verify. */
function makeJwt(expSec: number): string {
  const header  = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' })).replace(/=/g,'');
  const payload = btoa(JSON.stringify({ sub: 'root', exp: expSec })).replace(/=/g,'');
  return `${header}.${payload}.fakesig`;
}

describe('AuthService', () => {
  let service: AuthService;
  let routerSpy: jasmine.SpyObj<Router>;

  beforeEach(() => {
    routerSpy = jasmine.createSpyObj('Router', ['navigate', 'createUrlTree']);
    routerSpy.navigate.and.returnValue(Promise.resolve(true));

    TestBed.configureTestingModule({
      providers: [
        AuthService,
        { provide: Router, useValue: routerSpy },
      ]
    });

    service = TestBed.inject(AuthService);
  });

  afterEach(() => service.ngOnDestroy());

  it('should be created', () => {
    expect(service).toBeTruthy();
  });

  it('should return null token before login', () => {
    expect(service.token).toBeNull();
  });

  it('should store token in memory after valid login', () => {
    const exp = Math.floor(Date.now() / 1000) + 3600; // 1 hour from now
    const jwt = makeJwt(exp);
    expect(service.login(jwt)).toBeTrue();
    expect(service.token).toBe(jwt);
  });

  it('should reject expired token', () => {
    const exp = Math.floor(Date.now() / 1000) - 10; // 10 s in the past
    expect(service.login(makeJwt(exp))).toBeFalse();
    expect(service.token).toBeNull();
  });

  it('should reject malformed token', () => {
    expect(service.login('not.a.jwt')).toBeFalse();
  });

  it('should clear token on logout and navigate to /login', () => {
    const jwt = makeJwt(Math.floor(Date.now() / 1000) + 3600);
    service.login(jwt);
    service.logout();
    expect(service.token).toBeNull();
    expect(routerSpy.navigate).toHaveBeenCalledWith(['/login']);
  });

  it('should auto-logout and redirect when token expires', fakeAsync(() => {
    // Create a token expiring in 500 ms + buffer
    const expSec = Math.floor(Date.now() / 1000) + 1;
    service.login(makeJwt(expSec));
    // Advance time past expiry − buffer (buffer = 30 s, but timer fires at max(0, delay))
    // With a 1-second expiry, delay = 1000ms - 30000ms < 0 → logout immediately
    tick(0);
    expect(service.token).toBeNull();
    expect(routerSpy.navigate).toHaveBeenCalledWith(['/login']);
  }));

  it('should emit false from isAuthenticated$ before login', (done) => {
    service.isAuthenticated$.subscribe(v => {
      expect(v).toBeFalse();
      done();
    });
  });

  it('JWT must NOT be in localStorage or sessionStorage after login', () => {
    const jwt = makeJwt(Math.floor(Date.now() / 1000) + 3600);
    service.login(jwt);
    expect(localStorage.getItem(jwt)).toBeNull();
    expect(sessionStorage.getItem(jwt)).toBeNull();
    // Belt-and-suspenders: scan all keys
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)!;
      expect(localStorage.getItem(k)).not.toContain(jwt);
    }
  });
});
