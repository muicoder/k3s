//go:build windows
// +build windows

package containerd

import (
	"net"

	"github.com/containerd/containerd"
	"github.com/k3s-io/k3s/pkg/agent/templates"
	"github.com/k3s-io/k3s/pkg/daemons/config"
	util3 "github.com/k3s-io/k3s/pkg/util"
	pkgerrors "github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"k8s.io/kubernetes/pkg/kubelet/util"
)

// hostDirectory returns the name of the host dir for a given registry.
// Colons are not allowed in windows paths, so convert `:port` to `_port_`.
// Ref: https://github.com/containerd/containerd/blob/v1.7.25/remotes/docker/config/hosts.go#L291-L298
func hostDirectory(host string) string {
	if host, port, err := net.SplitHostPort(host); err == nil && port != "" {
		return host + "_" + port + "_"
	}
	return host
}

func getContainerdArgs(cfg *config.Node) []string {
	args := []string{
		"containerd",
		"-c", cfg.Containerd.Config,
	}
	return args
}

// SetupContainerdConfig generates the containerd.toml, using a template combined with various
// runtime configurations and registry mirror settings provided by the administrator.
func SetupContainerdConfig(cfg *config.Node) error {
	if cfg.SELinux {
		logrus.Warn("SELinux isn't supported on windows")
	}

	containerdConfig := templates.ContainerdConfig{
		NodeConfig:            cfg,
		DisableCgroup:         true,
		PrivateRegistryConfig: cfg.AgentConfig.Registry,
		NoDefaultEndpoint:     cfg.Containerd.NoDefault,
	}

	if err := writeContainerdConfig(cfg, containerdConfig); err != nil {
		return err
	}

	return writeContainerdHosts(cfg, containerdConfig)
}

func Client(address string) (*containerd.Client, error) {
	addr, _, err := util.GetAddressAndDialer(address)
	if err != nil {
		return nil, err
	}

	return containerd.New(addr)
}

func OverlaySupported(root string) error {
	return pkgerrors.WithMessagef(util3.ErrUnsupportedPlatform, "overlayfs is not supported")
}

func FuseoverlayfsSupported(root string) error {
	return pkgerrors.WithMessagef(util3.ErrUnsupportedPlatform, "fuse-overlayfs is not supported")
}

func StargzSupported(root string) error {
	return pkgerrors.WithMessagef(util3.ErrUnsupportedPlatform, "stargz is not supported")
}
