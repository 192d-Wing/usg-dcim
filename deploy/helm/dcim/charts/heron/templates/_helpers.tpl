{{- define "heron.fullname" -}}
{{- printf "%s-heron" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "heron.labels" -}}
app.kubernetes.io/name: heron
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: heron
app.kubernetes.io/part-of: dcim
{{- end -}}

{{- define "heron.selectorLabels" -}}
app.kubernetes.io/name: heron
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "heron.image" -}}
{{ .Values.global.image.registry }}/dcim-heron:{{ .Values.global.image.tag }}
{{- end -}}
