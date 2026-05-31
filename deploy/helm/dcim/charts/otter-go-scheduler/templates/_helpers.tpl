{{- define "otter-go-scheduler.fullname" -}}
{{- printf "%s-otter-go-scheduler" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "otter-go-scheduler.labels" -}}
app.kubernetes.io/name: otter-go-scheduler
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: otter-go-scheduler
app.kubernetes.io/part-of: dcim
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "otter-go-scheduler.selectorLabels" -}}
app.kubernetes.io/name: otter-go-scheduler
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- /*
Image reuses the otter-go image (multi-binary Containerfile). The
sub-chart overrides command: to point at /otter-go-scheduler so the
same release tag pins both deployments to the same code.
*/ -}}
{{- define "otter-go-scheduler.image" -}}
{{ .Values.global.image.registry }}/dcim-otter-go:{{ .Values.global.image.tag }}
{{- end -}}
