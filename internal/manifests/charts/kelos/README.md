# Kelos Helm Chart

The Kelos Helm chart is published as an OCI artifact in GHCR:

```bash
oci://ghcr.io/kelos-dev/charts/kelos
```

## First-Time Install

For a fresh Helm-managed install:

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version>
```

By default, the chart installs and upgrades the Kelos CRDs:

```yaml
crds:
  install: true
  keep: true
```

`crds.keep=true` keeps Helm from deleting the CRDs during chart uninstall.

## Migrating Existing CRDs Into Helm Ownership

If your cluster already has Kelos CRDs from `kelos install` or `kubectl apply`, your first Helm install or upgrade must choose one CRD owner.

### Option 1: Keep CRDs Managed Outside Helm

Use this if you want to continue managing CRDs with `kelos install` or another manifest-based workflow:

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version> \
  --set crds.install=false
```

With `crds.install=false`, Helm manages only the controller resources.

### Option 2: Adopt Existing CRDs Into Helm

Use this if you want Helm to manage future Kelos CRD upgrades by default.

```bash
RELEASE=kelos
NAMESPACE=kelos-system

for crd in \
  agentconfigs.kelos.dev \
  tasks.kelos.dev \
  taskspawners.kelos.dev \
  workspaces.kelos.dev
do
  kubectl label crd "$crd" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-name="$RELEASE" --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-namespace="$NAMESPACE" --overwrite
done

helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version>
```

After adoption, stop managing those CRDs with `kelos install` or `kubectl apply`.

## Upgrades

For Helm-managed installs where Helm already owns the CRDs, a normal upgrade is:

```bash
helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version>
```

If your cluster still manages CRDs outside Helm, keep using:

```bash
helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version> \
  --set crds.install=false
```

## Uninstall

To uninstall the Helm release:

```bash
helm uninstall kelos -n kelos-system
```

Because `crds.keep=true` by default, uninstalling the chart does not delete the Kelos CRDs.
