// src/app/core/guards/admin.guard.ts
//
// Feature 32 — role-gated CanActivate for admin-only routes.
//
// The admin guard is a defence-in-depth over the server's
// feature-37 authz: the server is still the authoritative
// enforcement layer (every /admin/* endpoint runs through
// `adminMiddleware`), but blocking non-admin users from even
// loading the component avoids surfacing a confusing 403 in
// the console + saves the network round-trip.
//
// A non-authenticated user redirects to /login; an
// authenticated non-admin redirects to the default landing
// route with a query param the UI can use to render a toast.
//
// IMPORTANT: A malicious client can forge a JWT with
// `role: "admin"` to pass THIS check alone — the browser
// does not verify signatures. That's fine because every
// actual admin action is server-gated; the guard's job is
// UI routing, not security. Document the contract here so
// future maintainers don't weaken the server side by
// leaning on the client check.

import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';
import { combineLatest } from 'rxjs';
import { map, take } from 'rxjs/operators';
import { AuthService } from '../services/auth.service';

export const adminGuard: CanActivateFn = () => {
  const auth = inject(AuthService);
  const router = inject(Router);

  return combineLatest([auth.isAuthenticated$, auth.isAdmin$]).pipe(
    take(1),
    map(([isAuth, isAdmin]) => {
      if (!isAuth) {
        return router.createUrlTree(['/login']);
      }
      if (!isAdmin) {
        // Landing page with a banner the shell can render.
        return router.createUrlTree(['/'], {
          queryParams: { forbidden: 'admin-required' },
        });
      }
      return true;
    }),
  );
};
