// src/app/core/interceptors/auth.interceptor.ts
//
// Functional HTTP interceptor (Angular 15+ style).
// Attaches Authorization: Bearer <token> to every outgoing HTTP request.
// On 401 Unauthorized, clears the in-memory token and redirects to /login.

import { HttpInterceptorFn, HttpRequest, HttpHandlerFn, HttpErrorResponse } from '@angular/common/http';
import { inject } from '@angular/core';
import { catchError, throwError } from 'rxjs';
import { AuthService } from '../services/auth.service';

export const authInterceptor: HttpInterceptorFn = (
  req: HttpRequest<unknown>,
  next: HttpHandlerFn
) => {
  const auth = inject(AuthService);
  const token = auth.token;

  const authedReq = token
    ? req.clone({ setHeaders: { Authorization: `Bearer ${token}` } })
    : req;

  return next(authedReq).pipe(
    catchError((err: unknown) => {
      if (err instanceof HttpErrorResponse && err.status === 401) {
        auth.onUnauthorized();
      }
      return throwError(() => err);
    })
  );
};
