{{- define "otter-go.fullname" -}}
{{- printf "%s-otter-go" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "otter-go.labels" -}}
app.kubernetes.io/name: otter-go
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: otter-go
app.kubernetes.io/part-of: dcim
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "otter-go.selectorLabels" -}}
app.kubernetes.io/name: otter-go
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "otter-go.image" -}}
{{ .Values.global.image.registry }}/dcim-otter-go:{{ .Values.global.image.tag }}
{{- end -}}
