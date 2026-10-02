#!/usr/bin/env bash
# Adds what the helm/v2-alpha plugin does not produce on its own:
#  - the ValidatingAdmissionPolicy objects (the plugin names extras files after
#    metadata.name, so a policy and its same-named binding collide and the
#    policy is lost); both are re-emitted here from dist/install.yaml behind
#    admissionPolicy.enabled (needs Kubernetes 1.30),
#  - the runner/orchestrator PodMonitor behind prometheus.enabled,
#  - the manager flags surfaced as values (and --hook-image), the chart-specific
#    values, and removal of the plugin's unpinned workflow and install-helm.
# Idempotent: safe to run on an already post-processed chart.
set -euo pipefail
chart=dist/chart
installer=dist/install.yaml
prefix=$(awk '/^namePrefix:/{print $2}' config/default/kustomization.yaml)
helper='claude-selfhosted-operator.resourceName'

# Admission policies and bindings, one file per object, names templated.
awk -v prefix="$prefix" -v helper="$helper" -v dir="$chart/templates/extras" '
function flush(   i, suffix, file) {
  if (kind == "ValidatingAdmissionPolicy" || kind == "ValidatingAdmissionPolicyBinding") {
    suffix = substr(name, length(prefix) + 1)
    file = dir "/" suffix (kind == "ValidatingAdmissionPolicy" ? "-policy.yaml" : "-binding.yaml")
    printf "" > file
    for (i = 1; i <= n; i++) print doc[i] > file
    close(file)
    system("rm -f \"" dir "/" suffix ".yaml\"")
  }
  n = 0; kind = ""; name = ""
}
/^---$/ { flush(); next }
{
  line = $0
  if (line ~ /^kind: /) kind = substr(line, 7)
  if (line ~ /^  namespace: /) next
  if (line ~ /^  (name|policyName): /) {
    key = line; sub(/: .*/, "", key)
    value = line; sub(/^  [a-zA-Z]+: /, "", value)
    if (key == "  name") name = value
    line = key ": {{ include \"" helper "\" (dict \"suffix\" \"" substr(value, length(prefix) + 1) "\" \"context\" $) }}"
  }
  doc[++n] = line
}
END { flush() }
' "$installer"

for f in "$chart"/templates/extras/*orchestrator-*.yaml; do
  [ -f "$f" ] || continue
  grep -q 'admissionPolicy.enabled' "$f" && continue
  { echo '{{- if .Values.admissionPolicy.enabled }}'; cat "$f"; echo '{{- end }}'; } > "$f.tmp" && mv "$f.tmp" "$f"
done

# PodMonitor for runner and orchestrator pods (mirrors config/prometheus/podmonitor.yaml).
mkdir -p "$chart/templates/prometheus"
cat > "$chart/templates/prometheus/runner-metrics-podmonitor.yaml" <<'EOP'
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  labels:
    app.kubernetes.io/managed-by: {{ .Release.Service }}
    app.kubernetes.io/name: {{ include "claude-selfhosted-operator.name" . }}
    helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
    app.kubernetes.io/instance: {{ .Release.Name }}
    {{- with .Values.prometheus.labels }}
    {{- with omit . "app.kubernetes.io/managed-by" "app.kubernetes.io/name" "helm.sh/chart" "app.kubernetes.io/instance" }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
    {{- end }}
  name: {{ include "claude-selfhosted-operator.resourceName" (dict "suffix" "runner-metrics" "context" $) }}
  namespace: {{ .Release.Namespace }}
spec:
  namespaceSelector:
    any: true
  selector:
    matchLabels:
      app.kubernetes.io/part-of: claude-code-self-hosted-runner
  podMetricsEndpoints:
    - port: health
      path: /metrics
      interval: 30s
EOP
for f in "$chart"/templates/prometheus/*podmonitor*.yaml; do
  [ -f "$f" ] || continue
  grep -q 'prometheus.enabled' "$f" && continue
  { echo '{{- if .Values.prometheus.enabled }}'; cat "$f"; echo '{{- end }}'; } > "$f.tmp" && mv "$f.tmp" "$f"
done

# Manager flags surfaced as values. --hook-image reuses the container image
# expression so the on-demand hook always matches the manager image.
manager="$chart/templates/manager/manager.yaml"
if ! grep -q -- '--hook-image=' "$manager"; then
  awk '
    /^        image: "/ { img = $0; sub(/^        image: "/, "", img); sub(/"$/, "", img) }
    { lines[++n] = $0 }
    END {
      for (i = 1; i <= n; i++) {
        print lines[i]
        if (lines[i] ~ /- --health-probe-bind-address=/) {
          print "        - \"--hook-image=" img "\""
          print "        - \"--watch-namespaces={{ .Values.watchNamespaces }}\""
          print "        {{- with .Values.tracing.endpoint }}"
          print "        - \"--tracing-endpoint={{ . }}\""
          print "        {{- end }}"
          print "        - \"--tracing-sample-ratio={{ .Values.tracing.sampleRatio }}\""
        }
      }
    }' "$manager" > "$manager.tmp" && mv "$manager.tmp" "$manager"
fi

# OPERATOR_IMAGE is superseded by --hook-image above; drop the literal the
# plugin copies from the installer so it cannot drift from manager.image.
awk '
  /^  env:$/ { getline n1; if (n1 ~ /^    - name: OPERATOR_IMAGE$/) { getline; print "  env: []"; next } print; print n1; next }
  { print }
' "$chart/values.yaml" > "$chart/values.yaml.tmp" && mv "$chart/values.yaml.tmp" "$chart/values.yaml"

# Plugin side files: its kind-based chart workflow installs unpinned tools and
# duplicates the lint in test.yml, and install-helm pipes curl into bash.
rm -f .github/workflows/test-chart.yml
if grep -q '^install-helm:' Makefile; then
  sed -i.bak -e '/^\.PHONY: install-helm$/,/^$/d' -e 's/^helm-deploy: install-helm /helm-deploy: /' Makefile && rm -f Makefile.bak
fi

grep -q '^admissionPolicy:' "$chart/values.yaml" || cat >> "$chart/values.yaml" <<'EOV'

# ValidatingAdmissionPolicy confining each orchestrator ServiceAccount to its
# own work orders. Requires Kubernetes 1.30 or later; disable on older clusters.
admissionPolicy:
  enabled: true

# Manager flags surfaced as values.
watchNamespaces: ""   # comma-separated; empty watches all namespaces
tracing:
  endpoint: ""        # OTLP gRPC endpoint; empty falls back to $OTEL_EXPORTER_OTLP_ENDPOINT, else off
  sampleRatio: 0.1
EOV
