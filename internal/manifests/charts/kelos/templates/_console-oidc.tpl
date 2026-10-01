{{- define "kelos.console.validateAuth" -}}
{{- $console := .Values.consoleServer -}}
{{- $auth := $console.auth -}}
{{- if eq $auth.mode "staticToken" -}}
{{- $_ := required "consoleServer.secretName is required when consoleServer.enabled=true" $console.secretName -}}
{{- if $auth.oidc -}}{{ fail "consoleServer.auth.oidc requires mode oidc" }}{{- end -}}
{{- else if eq $auth.mode "oidc" -}}
{{- if or $console.secretName $console.secureCookie (ne $console.tokenKey "token") -}}
{{- fail "static token settings cannot be used with consoleServer.auth.mode=oidc" -}}
{{- end -}}
{{- $oidc := $auth.oidc -}}
{{- range $key := list "issuerURL" "clientID" "redirectURL" "secretName" "usernamePrefix" "groupsPrefix" -}}
{{- $_ := required (printf "consoleServer.auth.oidc.%s is required" $key) (get $oidc $key) -}}
{{- end -}}
{{- range $key := list "issuerURL" "redirectURL" -}}
{{- $value := get $oidc $key -}}
{{- $url := urlParse $value -}}
{{- if or (ne $url.scheme "https") (not $url.host) $url.userinfo $url.query $url.fragment (regexMatch "[[:space:]]" $value) -}}
{{- fail (printf "consoleServer.auth.oidc.%s must be an HTTPS URL without credentials, query or fragment" $key) -}}
{{- end -}}
{{- end -}}
{{- if ne (urlParse $oidc.redirectURL).path "/oauth2/callback" -}}
{{- fail "consoleServer.auth.oidc.redirectURL must end with /oauth2/callback" -}}
{{- end -}}
{{- range $prefix := list $oidc.usernamePrefix $oidc.groupsPrefix -}}
{{- if or (hasPrefix "system:" $prefix) (hasPrefix $prefix "system:") (gt (len $prefix) 128) (ne (trim $prefix) $prefix) (regexMatch "[[:cntrl:],]" $prefix) -}}
{{- fail "OIDC identity prefixes must be valid non-reserved identity values" -}}
{{- end -}}
{{- end -}}
{{- if $oidc.reverseProxy -}}
{{- if not $oidc.trustedProxyIPs -}}{{ fail "OIDC reverseProxy requires trustedProxyIPs" }}{{- end -}}
{{- range $ip := $oidc.trustedProxyIPs -}}
{{- if or (not $ip) (regexMatch "/0+$|[[:space:]]" $ip) -}}{{ fail "OIDC trustedProxyIPs must contain explicit IPs or CIDRs, never catch-all ranges" }}{{- end -}}
{{- end -}}
{{- else if $oidc.trustedProxyIPs -}}
{{- fail "OIDC trustedProxyIPs requires reverseProxy" -}}
{{- end -}}
{{- $image := default "quay.io/oauth2-proxy/oauth2-proxy:v7.15.5" $oidc.image -}}
{{- if not (regexMatch "^.+:v[0-9]+\\.[0-9]+\\.[0-9]+(@sha256:[0-9a-f]{64})?$" $image) -}}
{{- fail "OIDC image must have an explicit vMAJOR.MINOR.PATCH tag" -}}
{{- end -}}
{{- $version := trimPrefix ":v" (regexFind ":v[0-9]+\\.[0-9]+\\.[0-9]+" $image) -}}
{{- if not (semverCompare ">=7.15.5" $version) -}}{{ fail "OIDC requires OAuth2 Proxy v7.15.5 or later" }}{{- end -}}
{{- else -}}
{{- fail "consoleServer.auth.mode must be staticToken or oidc" -}}
{{- end -}}
{{- end -}}

{{- define "kelos.console.proxyConfig" -}}
{{- $oidc := .Values.consoleServer.auth.oidc -}}
server:
  bindAddress: 0.0.0.0:4180
  secureBindAddress: "-"
upstreamConfig:
  upstreams:
    - id: console
      path: /
      uri: http://127.0.0.1:8080/
      proxyWebSockets: true
      timeout: 15m
providers:
  - id: console
    provider: oidc
    clientID: {{ $oidc.clientID | quote }}
    clientSecretFile: /var/run/secrets/oidc/client-secret
    scope: {{ default "openid profile email groups" $oidc.scope | quote }}
    code_challenge_method: S256
    oidcConfig:
      issuerURL: {{ $oidc.issuerURL | quote }}
      groupsClaim: {{ default "groups" $oidc.groupsClaim | quote }}
      insecureSkipNonce: false
      insecureSkipIssuerVerification: false
      insecureAllowUnverifiedEmail: false
injectRequestHeaders:
  - name: X-Kelos-User
    preserveRequestValue: false
    values:
      - claimSource:
          claim: user
  - name: X-Kelos-Groups
    preserveRequestValue: false
    values:
      - claimSource:
          claim: groups
  {{- range list "Authorization" "Cookie" "X-Forwarded-User" "X-Forwarded-Groups" "X-Forwarded-Email" "X-Forwarded-Preferred-Username" "X-Forwarded-Access-Token" "X-Auth-Request-User" "X-Auth-Request-Groups" "X-Auth-Request-Email" "X-Auth-Request-Access-Token" }}
  - name: {{ . }}
    preserveRequestValue: false
    values: []
  {{- end }}
injectResponseHeaders: []
{{- end -}}
