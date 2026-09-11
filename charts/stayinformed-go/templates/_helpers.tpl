{{- define "stayinformed-go.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "stayinformed-go.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "stayinformed-go.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "stayinformed-go.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "stayinformed-go.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "stayinformed-go.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stayinformed-go.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "stayinformed-go.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{- define "stayinformed-go.tokenContainer" -}}
- name: stayinformed-token
  image: {{ include "stayinformed-go.image" .root }}
  imagePullPolicy: {{ .root.Values.image.pullPolicy }}
  command:
    - /usr/local/bin/stayinformed-token
  args:
    - serve
    - --output-file=/run/stayinformed/access-token
    - --password-command=cat /var/run/secrets/stayinformed/password
    - --refresh-before={{ .root.Values.token.refreshBefore }}
    {{- if .root.Values.token.organization }}
    - --organization={{ .root.Values.token.organization }}
    {{- end }}
    - --locale={{ .root.Values.token.locale }}
  env:
    - name: STAYINFORMED_USERNAME
      valueFrom:
        secretKeyRef:
          name: {{ required "credentials.existingSecret is required" .root.Values.credentials.existingSecret }}
          key: {{ .root.Values.credentials.usernameKey }}
  readinessProbe:
    exec:
      command: [test, -s, /run/stayinformed/access-token]
    periodSeconds: 5
    timeoutSeconds: 2
    failureThreshold: 12
  resources:
{{ toYaml .resources | indent 4 }}
  securityContext:
{{ toYaml .root.Values.containerSecurityContext | indent 4 }}
  volumeMounts:
    - name: stayinformed-token
      mountPath: /run/stayinformed
    - name: stayinformed-credentials
      mountPath: /var/run/secrets/stayinformed
      readOnly: true
{{- end }}

{{- define "stayinformed-go.podPlacement" -}}
{{- with .Values.nodeSelector }}
nodeSelector:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
{{ toYaml . | indent 2 }}
{{- end }}
{{- end }}
