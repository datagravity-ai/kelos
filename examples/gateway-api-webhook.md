# Using Gateway API with Kelos Webhooks

This document explains how to configure Kelos webhook servers using the Gateway API instead of traditional Ingress resources.

## Prerequisites

1. **Gateway API CRDs installed** in your cluster:
   ```bash
   kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.0.0/standard-install.yaml
   ```

2. **Gateway Controller** deployed (choose one):
   - **Istio**: Follow [Istio Gateway API setup](https://istio.io/latest/docs/tasks/traffic-management/ingress/gateway-api/)
   - **Envoy Gateway**: Follow [Envoy Gateway installation](https://gateway.envoyproxy.io/latest/user/quickstart/)
   - **Kong**: Follow [Kong Gateway API setup](https://docs.konghq.com/kubernetes-ingress-controller/latest/guides/using-gateway-api/)
   - **Nginx Gateway Fabric**: Follow [Nginx Gateway setup](https://docs.nginx.com/nginx-gateway-fabric/)

## Configuration

### Basic Gateway Setup

```yaml
# values.yaml
webhookServer:
  sources:
    github:
      enabled: true
      secretName: github-webhook-secret

  # Use Gateway API instead of Ingress
  gateway:
    enabled: true
    gatewayClassName: "istio"  # Or your gateway class
    gatewayName: "kelos-webhook-gateway"
    host: "webhooks.your-domain.com"

    tls:
      enabled: true
      certificateRefs:
        - name: "webhook-tls-cert"
          kind: "Secret"
```

### Gateway Classes by Provider

| Provider | Gateway Class | Notes |
|----------|---------------|-------|
| Istio | `istio` | Built-in with Istio installation |
| Envoy Gateway | `eg` | Default class name |
| Kong | `kong` | Configured during Kong installation |
| Nginx Gateway Fabric | `nginx` | Default class name |

### TLS Configuration

#### Option 1: Pre-existing Certificate
```yaml
gateway:
  tls:
    enabled: true
    certificateRefs:
      - name: "my-tls-secret"
        kind: "Secret"
```

#### Option 2: cert-manager Integration
```yaml
gateway:
  enabled: true
  host: "webhooks.example.com"
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
  tls:
    enabled: true
    certificateRefs:
      - name: "webhook-tls-cert"  # cert-manager will create this
        kind: "Secret"
```

Then create a Certificate resource:
```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: webhook-tls-cert
  namespace: kelos-system
spec:
  secretName: webhook-tls-cert
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - webhooks.example.com
```

## Webhook Endpoints

When Gateway API is enabled, webhooks are available at:

- **GitHub**: `https://webhooks.your-domain.com/webhook/github`

## Deployment

```bash
# Deploy with Gateway API enabled
helm upgrade --install kelos ./internal/manifests/charts/kelos \
  --namespace kelos-system \
  --create-namespace \
  --values webhook-gateway-values.yaml
```

## Verification

1. **Check Gateway status**:
   ```bash
   kubectl get gateway kelos-webhook-gateway -n kelos-system
   kubectl describe gateway kelos-webhook-gateway -n kelos-system
   ```

2. **Check HTTPRoute status**:
   ```bash
   kubectl get httproute kelos-webhook-routes -n kelos-system
   kubectl describe httproute kelos-webhook-routes -n kelos-system
   ```

3. **Test webhook endpoint**:
   ```bash
   curl -I https://webhooks.your-domain.com/webhook/github
   ```

## Gateway API vs Ingress

| Feature | Ingress | Gateway API |
|---------|---------|-------------|
| **Maturity** | Stable, widely supported | Newer, growing adoption |
| **Flexibility** | Basic HTTP routing | Advanced routing, protocol support |
| **Multi-tenancy** | Limited | Built-in namespace isolation |
| **Traffic splitting** | Extension-specific | Native support |
| **Protocol support** | HTTP/HTTPS mainly | HTTP, HTTPS, TCP, UDP, gRPC |
| **Vendor lock-in** | Controller-specific annotations | Standardized API |

## Troubleshooting

### Gateway Not Ready
```bash
# Check gateway controller logs
kubectl logs -n istio-system deployment/istio-gateway

# Check gateway events
kubectl get events --field-selector involvedObject.name=kelos-webhook-gateway -n kelos-system
```

### Routes Not Working
```bash
# Check HTTPRoute status
kubectl describe httproute kelos-webhook-routes -n kelos-system

# Verify backend services exist
kubectl get svc -l app.kubernetes.io/component=webhook-github -n kelos-system
```

### TLS Issues
```bash
# Check certificate status (if using cert-manager)
kubectl get certificate webhook-tls-cert -n kelos-system
kubectl describe certificate webhook-tls-cert -n kelos-system

# Check secret exists
kubectl get secret webhook-tls-cert -n kelos-system
```
