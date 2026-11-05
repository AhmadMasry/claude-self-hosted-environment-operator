{{/*
Effective --watch-namespaces: the watchNamespaces value, or the release
namespace when rbac.namespaced=true and the value is empty, since a Role
cannot grant the cluster-wide list and watch an empty value asks for.
*/}}
{{- define "claude-selfhosted-operator.watchNamespaces" -}}
{{- if .Values.watchNamespaces }}{{ .Values.watchNamespaces }}{{ else if .Values.rbac.namespaced }}{{ .Release.Namespace }}{{ end }}
{{- end }}
