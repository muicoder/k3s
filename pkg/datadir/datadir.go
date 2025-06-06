package datadir

import (
	"path/filepath"

	"github.com/k3s-io/k3s/pkg/util/permissions"
	"github.com/k3s-io/k3s/pkg/version"
	pkgerrors "github.com/pkg/errors"
	"github.com/rancher/wrangler/v3/pkg/resolvehome"
)

var (
	DefaultDataDir     = "/var/lib/rancher/" + version.Program
	DefaultHomeDataDir = "${HOME}/.rancher/" + version.Program
	HomeConfig         = "${HOME}/.kube/" + version.Program + ".yaml"
	GlobalConfig       = "/etc/rancher/" + version.Program + "/" + version.Program + ".yaml"
)

func Resolve(dataDir string) (string, error) {
	return LocalHome(dataDir, false)
}

func LocalHome(dataDir string, forceLocal bool) (string, error) {
	if dataDir == "" {
		if permissions.IsPrivileged() == nil && !forceLocal {
			dataDir = DefaultDataDir
		} else {
			dataDir = DefaultHomeDataDir
		}
	}

	dataDir, err := resolvehome.Resolve(dataDir)
	if err != nil {
		return "", pkgerrors.WithMessagef(err, "resolving %s", dataDir)
	}

	return filepath.Abs(dataDir)
}
