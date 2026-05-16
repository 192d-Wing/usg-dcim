{{- define "badger.fullname" -}}
{{- printf "%s-badger" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "badger.labels" -}}
app.kubernetes.io/name: badger
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: badger
app.kubernetes.io/part-of: dcim
{{- end -}}

{{- define "badger.selectorLabels" -}}
app.kubernetes.io/name: badger
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "badger.image" -}}
{{ .Values.global.image.registry }}/badger:{{ .Values.global.image.tag }}
{{- end -}}
