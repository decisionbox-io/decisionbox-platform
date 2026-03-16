{{- define "decisionbox-api.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "decisionbox-api.fullname" -}}
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

{{- define "decisionbox-api.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{ include "decisionbox-api.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "decisionbox-api.selectorLabels" -}}
app: decisionbox-api
app.kubernetes.io/name: {{ include "decisionbox-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "decisionbox-api.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Auto-computed MongoDB URI when mongodb dependency is enabled.
*/}}
{{- define "decisionbox-api.mongodbURI" -}}
{{- $host := printf "%s-mongodb" (include "decisionbox-api.fullname" .) -}}
{{- $dbName := .Values.env.MONGODB_DB | default "decisionbox" -}}
{{- if and (index .Values "mongodb" "auth" "enabled") (index .Values "mongodb" "auth" "rootPassword") -}}
mongodb://root:{{ index .Values "mongodb" "auth" "rootPassword" }}@{{ $host }}:27017/{{ $dbName }}?authSource=admin
{{- else -}}
mongodb://{{ $host }}:27017/{{ $dbName }}
{{- end -}}
{{- end -}}
