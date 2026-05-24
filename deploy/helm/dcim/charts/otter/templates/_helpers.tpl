{{- define "otter.fullname" -}}
{{- printf "%s-otter" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "otter.labels" -}}
app.kubernetes.io/name: otter
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: otter
app.kubernetes.io/part-of: dcim
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "otter.selectorLabels" -}}
app.kubernetes.io/name: otter
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "otter.image" -}}
{{ .Values.global.image.registry }}/otter:{{ .Values.global.image.tag }}
{{- end -}}
