#!/usr/bin/env bash
# Regenerate the Helm chart's CRD and ClusterRole templates from the controller-gen
# output under config/, wrapping them in Helm conditionals (crds.enabled/keep,
# rbac.create). Kept as a script (not an inline mise task) so mise's task templating
# does not touch the Helm {{ }} braces. Run after `mise run manifests`.
set -euo pipefail

chart="charts/scm-metrics-exporter"

{
  echo '{{- if .Values.crds.enabled }}'
  for f in config/crd/bases/*.yaml; do
    # Inject a conditional keep annotation right after metadata.annotations.
    awk '/^  annotations:/ && ins == 0 {
      print
      print "    {{- if .Values.crds.keep }}"
      print "    helm.sh/resource-policy: keep"
      print "    {{- end }}"
      ins = 1
      next
    } { print }' "$f"
  done
  echo '{{- end }}'
} >"${chart}/templates/crds.yaml"

{
  echo '{{- if .Values.rbac.create }}'
  echo 'apiVersion: rbac.authorization.k8s.io/v1'
  echo 'kind: ClusterRole'
  echo 'metadata:'
  echo '  name: {{ include "scm-metrics-exporter.fullname" . }}-manager'
  echo '  labels:'
  echo '    {{- include "scm-metrics-exporter.labels" . | nindent 4 }}'
  sed -n '/^rules:/,$p' config/rbac/role.yaml
  echo '{{- end }}'
} >"${chart}/templates/clusterrole.yaml"

echo "chart:sync wrote ${chart}/templates/{crds.yaml,clusterrole.yaml}"
