{{- define "beagle.fullname" -}}
{{- printf "%s-beagle" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "beagle.labels" -}}
app.kubernetes.io/name: beagle
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: beagle
app.kubernetes.io/part-of: dcim
{{- end -}}

{{- define "beagle.selectorLabels" -}}
app.kubernetes.io/name: beagle
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "beagle.image" -}}
{{ .Values.global.image.registry }}/beagle:{{ .Values.global.image.tag }}
{{- end -}}
