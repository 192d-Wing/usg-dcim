{{- define "finch.fullname" -}}
{{- printf "%s-finch" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "finch.labels" -}}
app.kubernetes.io/name: finch
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: finch
app.kubernetes.io/part-of: dcim
{{- end -}}

{{- define "finch.selectorLabels" -}}
app.kubernetes.io/name: finch
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "finch.image" -}}
{{ .Values.global.image.registry }}/dcim-finch:{{ .Values.global.image.tag }}
{{- end -}}
