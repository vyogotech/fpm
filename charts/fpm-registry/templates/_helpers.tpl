{{/*
Expand the name of the chart.
*/}}
{{- define "fpm-registry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fpm-registry.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "fpm-registry.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "fpm-registry.labels" -}}
helm.sh/chart: {{ include "fpm-registry.chart" . }}
{{ include "fpm-registry.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "fpm-registry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fpm-registry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the write-path service.
*/}}
{{- define "fpm-registry.backendName" -}}
{{- printf "%s-backend" (include "fpm-registry.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Selector labels for the write-path service.

The read and write paths are separate deployments sharing one document root, so
their selectors must not overlap — otherwise the read Service would load-balance
GETs onto the write path, and the difference between them would stop being
observable at the point it matters.

The name carries the -backend suffix rather than relying on the component label
alone: a label selector matches supersets, so {name, instance} would select
these pods too. Distinguishing on name keeps the read path's existing selector
correct without editing it, which matters because a Deployment's selector is
immutable and an in-place upgrade could not change it.
*/}}
{{- define "fpm-registry.backendSelectorLabels" -}}
app.kubernetes.io/name: {{ include "fpm-registry.name" . }}-backend
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: backend
{{- end }}

{{/*
Common labels for the write-path service.
*/}}
{{- define "fpm-registry.backendLabels" -}}
helm.sh/chart: {{ include "fpm-registry.chart" . }}
{{ include "fpm-registry.backendSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "fpm-registry.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "fpm-registry.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
