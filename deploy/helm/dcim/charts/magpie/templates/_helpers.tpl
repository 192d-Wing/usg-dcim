{{- define "magpie.fullname" -}}
{{- printf "%s-magpie" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "magpie.labels" -}}
app.kubernetes.io/name: magpie
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: magpie
app.kubernetes.io/part-of: dcim
{{- end -}}

{{- define "magpie.selectorLabels" -}}
app.kubernetes.io/name: magpie
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "magpie.image" -}}
{{ .Values.global.image.registry }}/magpie:{{ .Values.global.image.tag }}
{{- end -}}
