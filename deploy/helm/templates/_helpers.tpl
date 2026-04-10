{{/*
Expand the name of the chart.
*/}}
{{- define "helion.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "helion.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label.
*/}}
{{- define "helion.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "helion.labels" -}}
helm.sh/chart: {{ include "helion.chart" . }}
app.kubernetes.io/name: {{ include "helion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Coordinator selector labels.
*/}}
{{- define "helion.coordinator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "helion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: coordinator
{{- end }}

{{/*
Node selector labels.
*/}}
{{- define "helion.node.selectorLabels" -}}
app.kubernetes.io/name: {{ include "helion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: node
{{- end }}

{{/*
Coordinator ServiceAccount name.
*/}}
{{- define "helion.coordinator.serviceAccountName" -}}
{{- if .Values.coordinator.serviceAccount.create }}
{{- default (printf "%s-coordinator" (include "helion.fullname" .)) .Values.coordinator.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.coordinator.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Node ServiceAccount name.
*/}}
{{- define "helion.node.serviceAccountName" -}}
{{- if .Values.node.serviceAccount.create }}
{{- default (printf "%s-node" (include "helion.fullname" .)) .Values.node.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.node.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Coordinator service DNS name (used by nodes to discover coordinator).
*/}}
{{- define "helion.coordinator.address" -}}
{{- printf "%s-coordinator:%d" (include "helion.fullname" .) (.Values.coordinator.service.grpcPort | int) }}
{{- end }}