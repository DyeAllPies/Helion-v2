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
  `],
})
export class RegisterDatasetDialogComponent {
  name = '';
  version = '';
  uri = '';
  sizeBytes?: number;
  sha256 = '';

  constructor(private ref: MatDialogRef<RegisterDatasetDialogComponent, DatasetRegisterRequest | undefined>) {}

  canSubmit(): boolean {
    return !!this.name && !!this.version && !!this.uri
        && (this.uri.startsWith('file://') || this.uri.startsWith('s3://'));
  }

  submit(): void {
    if (!this.canSubmit()) return;
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
    this.ref.close(req);
  }

  cancel(): void { this.ref.close(undefined); }
}
