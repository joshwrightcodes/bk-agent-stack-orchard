{{/* The name of every object of the release. */}}
{{- define "bkaso.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* The labels that select the pod. */}}
{{- define "bkaso.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* The labels of every object of the release. */}}
{{- define "bkaso.labels" -}}
{{ include "bkaso.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* The image, pinned by its digest when the values give one. */}}
{{- define "bkaso.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{/* The port of the health service, from the healthAddress of the configuration. */}}
{{- define "bkaso.healthPort" -}}
{{- $port := .Values.config.healthAddress | splitList ":" | last -}}
{{- if not (regexMatch "^[0-9]+$" $port) -}}
{{- fail (printf "config.healthAddress %q has no port" .Values.config.healthAddress) -}}
{{- end -}}
{{- $port -}}
{{- end -}}
