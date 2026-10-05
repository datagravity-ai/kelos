{{- define "kelos.console.validateAuth" -}}
{{- $console := .Values.consoleServer -}}
{{- $auth := $console.auth -}}
{{- if eq $auth.mode "staticToken" -}}
{{- $_ := required "consoleServer.secretName is required when consoleServer.enabled=true" $console.secretName -}}
{{- if or $auth.oidc $auth.github -}}{{ fail "proxy settings require mode oidc or github" }}{{- end -}}
{{- else if or (eq $auth.mode "oidc") (eq $auth.mode "github") -}}
{{- if or $console.secretName $console.secureCookie (ne $console.tokenKey "token") -}}
{{- fail "static token settings cannot be used with proxy authentication" -}}
{{- end -}}
{{- range $mode := list "oidc" "github" -}}
{{- if and (ne $mode $auth.mode) (get $auth $mode) -}}
{{- fail (printf "consoleServer.auth.%s requires mode %s" $mode $mode) -}}
{{- end -}}
{{- end -}}
{{- $provider := get $auth $auth.mode | default dict -}}
{{- range $key := list "clientID" "redirectURL" "secretName" "usernamePrefix" "groupsPrefix" -}}
{{- $_ := required (printf "consoleServer.auth.%s.%s is required" $auth.mode $key) (get $provider $key) -}}
{{- end -}}
{{- $urlKeys := list "redirectURL" -}}
{{- if eq $auth.mode "oidc" -}}
{{- $_ := required "consoleServer.auth.oidc.issuerURL is required" $provider.issuerURL -}}
{{- $urlKeys = append $urlKeys "issuerURL" -}}
{{- else if $provider.enterpriseURL -}}
{{- $urlKeys = append $urlKeys "enterpriseURL" -}}
{{- $path := (urlParse $provider.enterpriseURL).path -}}
{{- if and $path (ne $path "/") -}}{{ fail "GitHub enterpriseURL must be an HTTPS origin without a path" }}{{- end -}}
{{- end -}}
{{- range $key := $urlKeys -}}
{{- $value := get $provider $key -}}
{{- $url := urlParse $value -}}
{{- if or (ne $url.scheme "https") (not $url.host) $url.userinfo (contains "?" $value) (contains "#" $value) (regexMatch "[[:space:]]" $value) -}}
{{- fail (printf "consoleServer.auth.%s.%s must be an HTTPS URL without credentials, query or fragment" $auth.mode $key) -}}
{{- end -}}
{{- end -}}
{{- if ne (urlParse $provider.redirectURL).path "/oauth2/callback" -}}
{{- fail "Console redirectURL must end with /oauth2/callback" -}}
{{- end -}}
{{- range $prefix := list $provider.usernamePrefix $provider.groupsPrefix -}}
{{- if or (hasPrefix "system:" $prefix) (hasPrefix $prefix "system:") (gt (len $prefix) 128) (ne (trim $prefix) $prefix) (regexMatch "[[:cntrl:],]" $prefix) -}}
{{- fail "Console identity prefixes must be valid non-reserved identity values" -}}
{{- end -}}
{{- end -}}
{{- if $provider.reverseProxy -}}
{{- if not $provider.trustedProxyIPs -}}{{ fail "Console reverseProxy requires trustedProxyIPs" }}{{- end -}}
{{- range $ip := $provider.trustedProxyIPs -}}
{{- if or (not $ip) (regexMatch "/0+$|[[:space:]]" $ip) -}}{{ fail "Console trustedProxyIPs must contain explicit IPs or CIDRs, never catch-all ranges" }}{{- end -}}
{{- end -}}
{{- else if $provider.trustedProxyIPs -}}
{{- fail "Console trustedProxyIPs requires reverseProxy" -}}
{{- end -}}
{{- $image := default "quay.io/oauth2-proxy/oauth2-proxy:v7.15.5" $provider.image -}}
{{- if not (regexMatch "^.+:v[0-9]+\\.[0-9]+\\.[0-9]+(@sha256:[0-9a-f]{64})?$" $image) -}}
{{- fail "Console proxy image must have an explicit vMAJOR.MINOR.PATCH tag" -}}
{{- end -}}
{{- $version := trimPrefix ":v" (regexFind ":v[0-9]+\\.[0-9]+\\.[0-9]+" $image) -}}
{{- if not (semverCompare ">=7.15.5" $version) -}}{{ fail "Console requires OAuth2 Proxy v7.15.5 or later" }}{{- end -}}
{{- else -}}
{{- fail "consoleServer.auth.mode must be staticToken, oidc or github" -}}
{{- end -}}
{{- end -}}

{{- define "kelos.console.proxyConfig" -}}
{{- $auth := .Values.consoleServer.auth -}}
{{- $provider := get $auth $auth.mode -}}
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
    provider: {{ $auth.mode }}
    clientID: {{ $provider.clientID | quote }}
    clientSecretFile: /var/run/secrets/oauth2-proxy/client-secret
    {{- if eq $auth.mode "oidc" }}
    scope: {{ default "openid profile email groups" $provider.scope | quote }}
    code_challenge_method: S256
    oidcConfig:
      issuerURL: {{ $provider.issuerURL | quote }}
      groupsClaim: {{ default "groups" $provider.groupsClaim | quote }}
      insecureSkipNonce: false
      insecureSkipIssuerVerification: false
      insecureAllowUnverifiedEmail: false
    {{- else }}
    scope: "user:email read:org"
    {{- if $provider.enterpriseURL }}
    {{- $baseURL := trimSuffix "/" $provider.enterpriseURL }}
    loginURL: {{ printf "%s/login/oauth/authorize" $baseURL | quote }}
    redeemURL: {{ printf "%s/login/oauth/access_token" $baseURL | quote }}
    validateURL: {{ printf "%s/api/v3" $baseURL | quote }}
    {{- end }}
    {{- if $provider.org }}
    githubConfig:
      org: {{ $provider.org | quote }}
    {{- end }}
    {{- if $provider.caSecretName }}
    caFiles:
      - /var/run/secrets/oauth2-proxy-ca/ca.crt
    useSystemTrustStore: true
    {{- end }}
    {{- end }}
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
