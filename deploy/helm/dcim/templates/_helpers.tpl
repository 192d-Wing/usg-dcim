{{/*
  Umbrella-chart helpers. Per-animal helpers live in each subchart.
*/}}

{{- define "dcim.fullname" -}}
{{- printf "%s-dcim" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dcim.labels" -}}
app.kubernetes.io/name: dcim
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: dcim
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
  Subchart service-name resolver — mirrors the `<release>-<animal>` pattern
  the per-subchart fullname helpers emit. Use from umbrella templates that
  need to reference a subchart-owned Service (e.g. the Ingress backend).
  Pass the subchart name as `.name` and the root context as `.ctx`.
*/}}
{{- define "dcim.subchartService" -}}
{{- printf "%s-%s" .ctx.Release.Name .name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
