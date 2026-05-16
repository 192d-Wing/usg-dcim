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

{{/*
  Heron image: the chart currently runs the otter (Python) image under
  uvicorn for mTLS-terminated ingest. The Go binary at packages/heron is a
  Phase-1 perf port; once it grows TLS support, swap this to
  `.../heron:tag`.
*/}}
{{- define "heron.image" -}}
{{ .Values.global.image.registry }}/otter:{{ .Values.global.image.tag }}
{{- end -}}
