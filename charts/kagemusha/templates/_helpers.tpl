{{- define "kagemusha.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kagemusha.fullname" -}}
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

{{- define "kagemusha.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kagemusha.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kagemusha.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "kagemusha.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* The image tag: as set, or the appVersion with the engine suffix (the default chrome image has none) */}}
{{- define "kagemusha.imageTag" -}}
{{- if .Values.image.tag }}
{{- .Values.image.tag }}
{{- else if eq .Values.image.engine "chrome" }}
{{- .Chart.AppVersion }}
{{- else if eq .Values.image.engine "lightpanda" }}
{{- printf "%s-lightpanda" .Chart.AppVersion }}
{{- else }}
{{- fail "image.engine must be chrome or lightpanda" }}
{{- end }}
{{- end }}
