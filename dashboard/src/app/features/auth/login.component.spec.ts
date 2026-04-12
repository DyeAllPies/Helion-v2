// src/app/features/auth/login.component.spec.ts
import { ComponentFixture, TestBed, fakeAsync, tick } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';

import { LoginComponent } from './login.component';
import { AuthService } from '../../core/services/auth.service';

describe('LoginComponent', () => {
  let fixture:   ComponentFixture<LoginComponent>;
  let component: LoginComponent;
  let authSpy:   jasmine.SpyObj<AuthService>;
  let router:    Router;

  beforeEach(async () => {
    authSpy = jasmine.createSpyObj('AuthService', ['login'], { token: null });

    await TestBed.configureTestingModule({
      imports: [LoginComponent],
      providers: [
        provideRouter([{ path: 'nodes', component: {} as never }]),
        provideAnimations(),
        { provide: AuthService, useValue: authSpy },
      ],
    }).compileComponents();

    fixture   = TestBed.createComponent(LoginComponent);
    component = fixture.componentInstance;
    router    = TestBed.inject(Router);
    fixture.detectChanges();
  });

  it('should create', () => expect(component).toBeTruthy());

  it('submit button should be disabled when token is empty', () => {
    component.tokenInput = '';
    fixture.detectChanges();
    const btn: HTMLButtonElement = fixture.nativeElement.querySelector('.login-btn');
    expect(btn.disabled).toBeTrue();
  });

  it('should call auth.login with trimmed token on submit', fakeAsync(() => {
    authSpy.login.and.returnValue(true);
    component.tokenInput = '  valid.jwt.token  ';
    component.submit();
    tick(300);
    expect(authSpy.login).toHaveBeenCalledWith('valid.jwt.token');
  }));

  it('should show error message on invalid token', fakeAsync(() => {
    authSpy.login.and.returnValue(false);
    component.tokenInput = 'bad.token';
    component.submit();
    tick(300);
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.error-msg')).toBeTruthy();
  }));

  it('ngOnInit should redirect to /nodes if already authenticated', () => {
    // Re-create with a spy that reports a token
    const authedSpy = jasmine.createSpyObj('AuthService', ['login'], { token: 'existing.jwt.token' });
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      imports: [LoginComponent],
      providers: [
        provideRouter([{ path: 'nodes', component: {} as never }]),
        provideAnimations(),
        { provide: AuthService, useValue: authedSpy },
      ],
    });
    const f = TestBed.createComponent(LoginComponent);
    const r = TestBed.inject(Router);
    const rSpy = spyOn(r, 'navigate').and.returnValue(Promise.resolve(true));
    f.detectChanges();
    expect(rSpy).toHaveBeenCalledWith(['/nodes']);
    f.destroy();
  });

  it('should navigate to /nodes on successful login', fakeAsync(() => {
    authSpy.login.and.returnValue(true);
    const navSpy = spyOn(router, 'navigate').and.returnValue(Promise.resolve(true));
    component.tokenInput = 'valid.jwt.token';
    component.submit();
    tick(300);
    expect(navSpy).toHaveBeenCalledWith(['/nodes']);
  }));
});
