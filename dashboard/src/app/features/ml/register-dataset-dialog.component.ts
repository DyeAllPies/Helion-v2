// src/app/features/ml/register-dataset-dialog.component.ts
//
// Modal form for POST /api/datasets. Light client-side validation
// (required fields, URI-scheme hint); the coordinator does the
// authoritative validation and returns a 400 the parent component
// surfaces verbatim.

import { Component } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatButtonModule } from '@angular/material/button';

import { DatasetRegisterRequest } from '../../shared/models';

@Component({
  selector: 'app-register-dataset-dialog',
  standalone: true,
  imports: [
    CommonModule, FormsModule, MatDialogModule,
    MatFormFieldModule, MatInputModule, MatButtonModule,
  ],
  template: `
<h2 mat-dialog-title>Register dataset</h2>
<mat-dialog-content>
  <p class="hint">Metadata only — the artifact bytes are stored in the configured artifact backend.</p>

  <mat-form-field appearance="outline" class="full">
    <mat-label>Name</mat-label>
    <input matInput [(ngModel)]="name" required autocomplete="off" name="name" pattern="[a-z0-9._-]+" />
    <mat-hint>lower-case letters, digits, ._- only</mat-hint>
  </mat-form-field>

  <mat-form-field appearance="outline" class="full">
    <mat-label>Version</mat-label>
    <input matInput [(ngModel)]="version" required autocomplete="off" name="version" />
    <mat-hint>e.g. v1.0.0 — immutable; re-registering the same version is rejected</mat-hint>
  </mat-form-field>

  <mat-form-field appearance="outline" class="full">
    <mat-label>URI</mat-label>
    <input matInput [(ngModel)]="uri" required autocomplete="off" name="uri" />
    <mat-hint>file:// or s3:// — other schemes rejected server-side</mat-hint>
  </mat-form-field>

  <mat-form-field appearance="outline" class="full">
    <mat-label>Size (bytes, optional)</mat-label>
    <input matInput type="number" [(ngModel)]="sizeBytes" name="size_bytes" />
  </mat-form-field>

  <mat-form-field appearance="outline" class="full">
    <mat-label>SHA-256 (optional, lower-hex)</mat-label>
    <input matInput [(ngModel)]="sha256" autocomplete="off" name="sha256" pattern="[0-9a-f]{64}" />
  </mat-form-field>

  <mat-form-field appearance="outline" class="full">
    <mat-label>Tags (optional, comma-separated key:value)</mat-label>
    <input matInput [(ngModel)]="tagsRaw" autocomplete="off" name="tags"
           placeholder="team:ml, env:prod" />
    <mat-hint>Each entry is "key:value". Server enforces k8s-shape (32 entries, 63-char keys, 253-char values).</mat-hint>
  </mat-form-field>
  <div class="tag-error" *ngIf="tagError">{{ tagError }}</div>
</mat-dialog-content>
<mat-dialog-actions align="end">
  <button mat-button (click)="cancel()">Cancel</button>
  <button mat-flat-button color="primary"
          [disabled]="!canSubmit()"
          (click)="submit()">Register</button>
</mat-dialog-actions>
  `,
  styles: [`
    .full { width: 100%; margin-bottom: 8px; display: block; }
    .hint { font-size: 12px; color: var(--color-muted); margin-bottom: 16px; }
    .tag-error {
      color: var(--color-error);
      font-size: 11px;
      margin: -4px 0 8px 0;
    }
  `],
})
export class RegisterDatasetDialogComponent {
  name = '';
  version = '';
  uri = '';
  sizeBytes?: number;
  sha256 = '';
  /** Raw text from the tags input — comma-separated `key:value` pairs. */
  tagsRaw = '';
  /** Surfaced inline when tagsRaw fails parsing; cleared on submit/edit. */
  tagError = '';

  constructor(private ref: MatDialogRef<RegisterDatasetDialogComponent, DatasetRegisterRequest | undefined>) {}

  canSubmit(): boolean {
    return !!this.name && !!this.version && !!this.uri
        && (this.uri.startsWith('file://') || this.uri.startsWith('s3://'));
  }

  submit(): void {
    if (!this.canSubmit()) return;
    let parsedTags: Record<string, string> | undefined;
    if (this.tagsRaw.trim() !== '') {
      const out = parseTags(this.tagsRaw);
      if (out.error) {
        this.tagError = out.error;
        return;
      }
      parsedTags = out.tags;
    }

    const req: DatasetRegisterRequest = {
      name: this.name.trim(),
      version: this.version.trim(),
      uri: this.uri.trim(),
    };
    if (this.sizeBytes !== undefined && this.sizeBytes !== null && !Number.isNaN(this.sizeBytes)) {
      req.size_bytes = Number(this.sizeBytes);
    }
    if (this.sha256.trim() !== '') {
      req.sha256 = this.sha256.trim().toLowerCase();
    }
    if (parsedTags) {
      req.tags = parsedTags;
    }
    this.ref.close(req);
  }

  cancel(): void { this.ref.close(undefined); }
}

/**
 * parseTags converts a comma-separated `key:value` string into a tag
 * map. Trims whitespace; empty-after-trim entries are skipped (so a
 * trailing comma is forgiving). Returns an error string on the first
 * malformed entry instead of throwing — the dialog surfaces it
 * inline. Server-side validation is the authoritative gate; this
 * client-side check is purely a UX one.
 */
export function parseTags(raw: string): { tags?: Record<string, string>; error?: string } {
  const out: Record<string, string> = {};
  for (const part of raw.split(',')) {
    const trimmed = part.trim();
    if (trimmed === '') continue;
    const idx = trimmed.indexOf(':');
    if (idx <= 0 || idx === trimmed.length - 1) {
      return { error: `tag "${trimmed}" must be key:value (non-empty key and value)` };
    }
    const key = trimmed.slice(0, idx).trim();
    const value = trimmed.slice(idx + 1).trim();
    if (key === '' || value === '') {
      return { error: `tag "${trimmed}" must be key:value (non-empty key and value)` };
    }
    out[key] = value;
  }
  return { tags: Object.keys(out).length > 0 ? out : undefined };
}
