{{/*
  Per-DnsServer release helpers.

  fullname: "<release>-<server.name>" so multiple servers on one site
  cluster don't collide. role/server/fabric labels are emitted on every
  resource so operators can `kubectl get -l dcim.io/dns-role=recursive`.
*/}}

{{- define "dnsSite.fullname" -}}
{{- $name := .Values.server.name | default .Release.Name -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dnsSite.labels" -}}
app.kubernetes.io/name: dns-site
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: dcim
app.kubernetes.io/managed-by: {{ .Release.Service }}
dcim.io/dns-server-id: {{ .Values.server.id | quote }}
dcim.io/dns-server-name: {{ .Values.server.name | quote }}
dcim.io/dns-role: {{ .Values.server.role | quote }}
dcim.io/fabric-id: {{ .Values.server.fabricId | quote }}
dcim.io/site-id: {{ .Values.server.siteId | quote }}
{{- end -}}

{{- define "dnsSite.selectorLabels" -}}
app.kubernetes.io/name: dns-site
app.kubernetes.io/instance: {{ .Release.Name }}
dcim.io/dns-server-id: {{ .Values.server.id | quote }}
{{- end -}}

{{- define "dnsSite.corednsImage" -}}
{{- if eq .Values.server.role "auth" -}}
{{- printf "%s:%s" .Values.image.auth.repository .Values.image.auth.tag -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.recursive.repository .Values.image.recursive.tag -}}
{{- end -}}
{{- end -}}

{{- define "dnsSite.collectorImage" -}}
{{- printf "%s:%s" .Values.image.collector.repository .Values.image.collector.tag -}}
{{- end -}}
