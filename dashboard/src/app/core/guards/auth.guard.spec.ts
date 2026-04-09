// src/app/core/guards/auth.guard.spec.ts
import { TestBed, fakeAsync } from '@angular/core/testing';
import { UrlTree, provideRouter } from '@angular/router';
import { BehaviorSubject, isObservable, firstValueFrom } from 'rxjs';

import { authGuard } from './auth.guard';
import { AuthService } from '../services/auth.service';

describe('authGuard', () => {
  let authSpy: jasmine.SpyObj<AuthService>;
  let isAuth$: BehaviorSubject<boolean>;

  const runGuard = () =>
    TestBed.runInInjectionContext(() => authGuard({} as never, {} as never));

  // Helper: resolves whether the guard returned Observable, Promise, or plain value
  const resolveGuard = async (): Promise<unknown> => {
    const result = runGuard();
    if (isObservable(result)) return firstValueFrom(result);
    return result;
  };

  beforeEach(() => {
    isAuth$ = new BehaviorSubject(false);
    authSpy = jasmine.createSpyObj('AuthService', [], {
      isAuthenticated$: isAuth$.asObservable(),
    });

    TestBed.configureTestingModule({
      providers: [
        provideRouter([{ path: 'login', component: {} as never }]),
        { provide: AuthService, useValue: authSpy },
      ],
    });
  });

  it('should allow navigation when authenticated', fakeAsync(async () => {
    isAuth$.next(true);
    const result = await resolveGuard();
    expect(result).toBeTrue();
  }));

  it('should redirect to /login when not authenticated', fakeAsync(async () => {
    isAuth$.next(false);
    const result = await resolveGuard();
    expect(result instanceof UrlTree).toBeTrue();
    expect((result as UrlTree).toString()).toContain('/login');
  }));
});
