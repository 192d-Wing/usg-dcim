{{- define "otter-worker.fullname" -}}
{{- printf "%s-otter-worker" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "otter-worker.labels" -}}
app.kubernetes.io/name: otter-worker
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: otter-worker
app.kubernetes.io/part-of: dcim
{{- end -}}

{{- define "otter-worker.selectorLabels" -}}
app.kubernetes.io/name: otter-worker
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "otter-worker.image" -}}
{{ .Values.global.image.registry }}/otter:{{ .Values.global.image.tag }}
{{- end -}}
