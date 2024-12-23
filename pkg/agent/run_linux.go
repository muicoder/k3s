//go:build linux
// +build linux

package agent

import (
	"github.com/k3s-io/k3s/pkg/version"
	"os"
	"path/filepath"

	"github.com/k3s-io/k3s/pkg/cli/cmds"
	"github.com/k3s-io/k3s/pkg/daemons/config"
)

// setupCriCtlConfig creates the crictl config file and populates it
// with the given data from config.
func setupCriCtlConfig(cfg cmds.Agent, nodeConfig *config.Node) error {
	cre := nodeConfig.ContainerRuntimeEndpoint
	if cre == "" {
		switch {
		case cfg.Docker:
			cre = "unix:///run/" + version.Program + "/cri-dockerd/cri-dockerd.sock"
		default:
			cre = "unix:///run/" + version.Program + "/containerd/containerd.sock"
		}
	}

	agentConfDir := filepath.Join(cfg.DataDir, "agent", "etc")
	if _, err := os.Stat(agentConfDir); os.IsNotExist(err) {
		if err := os.MkdirAll(agentConfDir, 0700); err != nil {
			return err
		}
	}

	// Send to node struct the value from cli/config default runtime
	if cfg.DefaultRuntime != "" {
		nodeConfig.DefaultRuntime = cfg.DefaultRuntime
	}

	crp := "runtime-endpoint: " + cre + "\n"
	ise := nodeConfig.ImageServiceEndpoint
	if ise != "" && ise != cre {
		crp += "image-endpoint: " + cre + "\n"
	}
	return os.WriteFile(agentConfDir+"/crictl.yaml", []byte(crp), 0600)
}
