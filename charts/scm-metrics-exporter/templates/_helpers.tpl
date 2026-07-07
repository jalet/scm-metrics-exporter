{{/* Chart name, overridable. */}}
{{- define "scm-metrics-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "scm-metrics-exporter.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "scm-metrics-exporter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "scm-metrics-exporter.labels" -}}
helm.sh/chart: {{ include "scm-metrics-exporter.chart" . }}
{{ include "scm-metrics-exporter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: scm-metrics-exporter
{{- end -}}

{{- define "scm-metrics-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "scm-metrics-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: operator
{{- end -}}

{{- define "scm-metrics-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "scm-metrics-exporter.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Operator image reference (digest wins over tag). */}}
{{- define "scm-metrics-exporter.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Exporter image the operator injects; defaults to the operator image. */}}
{{- define "scm-metrics-exporter.exporterImage" -}}
{{- if .Values.exporterImage.repository -}}
{{- $tag := .Values.exporterImage.tag | default .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.exporterImage.repository $tag -}}
{{- else -}}
{{- include "scm-metrics-exporter.image" . -}}
{{- end -}}
{{- end -}}
