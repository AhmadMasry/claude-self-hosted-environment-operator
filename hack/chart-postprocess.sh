#!/usr/bin/env bash
# Adds what the helm/v2-alpha plugin does not produce on its own:
#  - the ValidatingAdmissionPolicy objects (the plugin names extras files after
#    metadata.name, so a policy and its same-named binding collide and the
#    policy is lost); both are re-emitted here from dist/install.yaml behind
#    admissionPolicy.enabled (needs Kubernetes 1.30),
#  - the runner/orchestrator PodMonitor behind prometheus.enabled,
#  - the manager flags surfaced as values (and --hook-image,
#    --apiserver-endpoints), the chart-specific
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

# --apiserver-endpoints only when the list is set; empty keeps the
# default/kubernetes Endpoints read.
if ! grep -q -- '--apiserver-endpoints=' "$manager"; then
  awk '
    { print }
    /- "--tracing-sample-ratio=/ {
      print "        {{- with .Values.networkPolicy.apiServerEndpoints }}"
      print "        - \"--apiserver-endpoints={{ join \",\" . }}\""
      print "        {{- end }}"
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

# The orchestrator's API server addresses, under the plugin's networkPolicy key.
if ! grep -q '^  apiServerEndpoints:' "$chart/values.yaml"; then
  awk '
    { print }
    /^networkPolicy:$/ { inblock = 1; next }
    inblock && /^  enabled: / {
      print ""
      print "  ## API server addresses (ip:port, IPv6 bracketed) for the orchestrator egress"
      print "  ## NetworkPolicy of on-demand environments. Empty reads the default/kubernetes"
      print "  ## Endpoints, which rbac.namespaced=true does not grant: in that mode this list"
      print "  ## is required whenever an on-demand environment enables spec.runner.networkPolicy."
      print "  ## Find the values with: kubectl get endpoints kubernetes -n default"
      print "  apiServerEndpoints: []"
      inblock = 0
    }' "$chart/values.yaml" > "$chart/values.yaml.tmp" && mv "$chart/values.yaml.tmp" "$chart/values.yaml"
fi

grep -q '^admissionPolicy:' "$chart/values.yaml" || cat >> "$chart/values.yaml" <<'EOV'

admissionPolicy:
  # -- Install the ValidatingAdmissionPolicy that confines each on-demand orchestrator
  # ServiceAccount to its own work orders. Needs Kubernetes 1.30 or later; set to
  # false on older clusters.
  enabled: true

# -- Namespaces the manager watches, comma-separated; empty watches all namespaces
# (manager flag --watch-namespaces).
watchNamespaces: ""
tracing:
  # -- OTLP gRPC endpoint for traces; empty falls back to $OTEL_EXPORTER_OTLP_ENDPOINT,
  # and tracing stays off when neither is set (manager flag --tracing-endpoint).
  endpoint: ""
  # -- Fraction of root spans sampled; the manager flag is tracing-sample-ratio.
  sampleRatio: 0.1
EOV

# Chart metadata the plugin leaves generic: description, home, sources,
# maintainers and keywords, read by Artifact Hub and the GHCR package page.
chart_meta() {
  sed -i.bak \
    -e 's|^description: .*|description: Runs Claude Code self-hosted environments on Kubernetes as fixed or on-demand runner fleets|' \
    "$chart/Chart.yaml" && rm -f "$chart/Chart.yaml.bak"
  grep -q '^home:' "$chart/Chart.yaml" || cat >> "$chart/Chart.yaml" <<'EOM'

home: https://github.com/AhmadMasry/claude-self-hosted-environment-operator
sources:
  - https://github.com/AhmadMasry/claude-self-hosted-environment-operator
maintainers:
  - name: Ahmad Al-Masry
    url: https://github.com/AhmadMasry
EOM
  grep -q '  - claude-code' "$chart/Chart.yaml" || sed -i.bak \
    -e 's|^  - operator$|  - operator\n  - claude\n  - claude-code\n  - anthropic\n  - self-hosted\n  - ai-agents|' \
    "$chart/Chart.yaml" && rm -f "$chart/Chart.yaml.bak"
}
chart_meta

# The plugin comments values with "## text" blocks; helm-docs reads "# -- text"
# (continuation lines "# text"). Convert each block that precedes a value, and
# keep blocks that precede a commented-out example or a blank line as plain
# comments. Idempotent: lines already in "# --" form are left alone.
awk '
  function emit(desc,    i) {
    for (i = 1; i <= n; i++) print ind (i == 1 && desc ? "# -- " : "# ") buf[i]
    n = 0
  }
  /^[ \t]*##([ \t]|$)/ || /^[ \t]*# [A-Z]/ {
    # "## text" blocks, and single-hash prose ("# Capitalised text", never a
    # commented-out key, which is lower-case).
    match($0, /^[ \t]*/); ind = substr($0, 1, RLENGTH)
    t = $0; sub(/^[ \t]*##?[ \t]?/, "", t)
    if (t != "") buf[++n] = t
    next
  }
  {
    # A description is attached only to a leaf value; a block before a parent
    # key (nothing after the colon), a commented-out example or a blank line
    # stays a plain comment, or helm-docs would print the whole object as the
    # default of the parent.
    if (n > 0) emit(!($0 ~ /^[ \t]*#/ || $0 ~ /^[ \t]*$/ || $0 ~ /^[ \t]*[A-Za-z0-9_.-]+:[ \t]*$/))
    print
  }
  END { if (n > 0) emit(0) }
' "$chart/values.yaml" > "$chart/values.yaml.tmp" && mv "$chart/values.yaml.tmp" "$chart/values.yaml"
