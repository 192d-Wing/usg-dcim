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

{{/*
  Postgres DSN as pgx wants it. The umbrella value
  `global.postgresql.dsn` is authored in SQLAlchemy form
  (`postgresql+asyncpg://…`) because Python otter is the canonical
  consumer. Go services (otter-go, heron, magpie, beagle) use pgx
  which rejects the `+asyncpg` driver hint. Stripping it here keeps
  the one-DSN-per-deploy contract while letting Go pods parse it.

  Idempotent: when the operator already supplies `postgres://…`
  (or `postgresql://…`) the regex doesn't match and the value
  passes through unchanged. `^` anchors at start so a password
  containing the substring `+asyncpg` survives.
*/}}
{{- define "dcim.goDsn" -}}
{{- regexReplaceAll "^postgresql\\+asyncpg" .Values.global.postgresql.dsn "postgres" -}}
{{- end -}}
