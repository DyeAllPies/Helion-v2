# Deferred: Dataset upload-via-browser modal

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 18 — ML dashboard module](../implemented/18-ml-dashboard-module.md)

## Context

Feature 18's spec called for a "register-via-upload modal" on the Datasets view. What shipped is a register-via-form modal that accepts a URI (`s3://...` or `file://...`) — the user must already have placed the bytes in the artifact store before registering. The end-to-end "browse, select a file from your laptop, click Upload, see it appear in the registry" flow is not implemented.

## Why deferred

Browser-driven upload to a server-side artifact store needs three pieces that the slice would otherwise have to introduce all at once:

1. **A signed-URL endpoint on the coordinator.** `POST /api/datasets/upload-url` returning a one-shot URL (S3 PUT for the s3 backend, a local-only proxy for the file backend). This is in flight as a separate `docs/SECURITY.md` § "Signed URLs for direct node→S3 transfer" item — same primitive, different consumer.
2. **Multipart upload handling.** Datasets above ~5 GiB need S3 multipart upload semantics; the client-side library and the server's part-stitch logic are non-trivial.
3. **Browser-side progress + cancel UX.** A real upload modal needs progress, cancel, retry-from-byte-N, and resilience against network jitters. These are the bits an MVP can least afford to skip — a half-working uploader is worse than no uploader.

Until the signed-URL endpoint lands, the URI-based form covers the operator workflow ("here's where I put the bytes, register them") and the dashboard isn't blocking on bytes-routing. A user who needs upload today shells out to `aws s3 cp` then opens the dialog.

## Revisit trigger

- The signed-URL endpoint lands (whether motivated by direct node→S3 transfer or by this slice). Once that primitive exists, the upload modal is mostly a thin client over it.
- Or: an operator reports the URI-only form is unworkable for their flow (e.g. they need to register hundreds of one-off datasets and the shell-then-form sequence is friction enough to matter).

When implemented, the file will be `dashboard/src/app/features/ml/upload-dataset-dialog.component.ts` and the existing register-dataset dialog should grow a "Switch to upload" toggle so the two paths share a single entry point.
