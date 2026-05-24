{{- define "dhcpSite.fullname" -}}
{{- $name := .Values.server.name | default .Release.Name -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dhcpSite.labels" -}}
app.kubernetes.io/name: dhcp-site
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: dcim
app.kubernetes.io/managed-by: {{ .Release.Service }}
dcim.io/dhcp-server-id: {{ .Values.server.id | quote }}
dcim.io/dhcp-server-name: {{ .Values.server.name | quote }}
dcim.io/fabric-id: {{ .Values.server.fabricId | quote }}
{{- end -}}

{{- define "dhcpSite.selectorLabels" -}}
app.kubernetes.io/name: dhcp-site
app.kubernetes.io/instance: {{ .Release.Name }}
dcim.io/dhcp-server-id: {{ .Values.server.id | quote }}
{{- end -}}
