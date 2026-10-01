package manifests

import (
	"embed"
	"io/fs"
)

//go:embed install-crd.yaml
var InstallCRD []byte

//go:embed charts/kelos charts/kelos/templates/_console-oidc.tpl
var chartFS embed.FS

// ChartFS is a filesystem rooted at the embedded Helm chart directory.
// Callers see Chart.yaml, values.yaml, and templates/ at the root level.
var ChartFS fs.FS

func init() {
	var err error
	ChartFS, err = fs.Sub(chartFS, "charts/kelos")
	if err != nil {
		panic("embedding chart filesystem: " + err.Error())
	}
}
