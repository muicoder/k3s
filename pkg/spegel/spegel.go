package spegel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/k3s-io/k3s/pkg/agent/https"
	"github.com/k3s-io/k3s/pkg/clientaccess"
	"github.com/k3s-io/k3s/pkg/daemons/config"
	"github.com/k3s-io/k3s/pkg/server/auth"
	"github.com/k3s-io/k3s/pkg/version"
	"github.com/rancher/dynamiclistener/cert"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/gorilla/mux"
	leveldb "github.com/ipfs/go-ds-leveldb"
	ipfslog "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds"
	pkgerrors "github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spegel-org/spegel/pkg/metrics"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/routing"
	"github.com/spegel-org/spegel/pkg/state"
	"k8s.io/component-base/metrics/legacyregistry"
)

// DefaultRegistry is the default instance of a Spegel distributed registry
var DefaultRegistry = &Config{
	Bootstrapper: NewSelfBootstrapper(),
	Router: func(context.Context, *config.Node) (*mux.Router, error) {
		return nil, errors.New("not implemented")
	},
}

var (
	P2pAddressAnnotation = "p2p." + version.Program + ".cattle.io/node-address"
	P2pEnabledLabel      = "p2p." + version.Program + ".cattle.io/enabled"
	P2pPortEnv           = version.ProgramUpper + "_P2P_PORT"
	P2pEnableLatestEnv   = version.ProgramUpper + "_P2P_ENABLE_LATEST"

	resolveLatestTag = false

	// Agents request a list of peers when joining, and then again periodically afterwards.
	// Limit the number of concurrent peer list requests that will be served simultaneously.
	maxNonMutatingPeerInfoRequests = 20 // max concurrent get/list/watch requests
	maxMutatingPeerInfoRequests    = 0  // max concurrent other requests; not used
)

// Config holds fields for a distributed registry
type Config struct {
	ClientCAFile   string
	ClientCertFile string
	ClientKeyFile  string

	ServerCAFile   string
	ServerCertFile string
	ServerKeyFile  string

	// ExternalAddress is the address for other nodes to connect to the registry API.
	ExternalAddress string

	// InternalAddress is the address for the local containerd instance to connect to the registry API.
	InternalAddress string

	// RegistryPort is the port for the registry API.
	RegistryPort string

	// PSK is the preshared key required to join the p2p network.
	PSK []byte

	// Bootstrapper is the bootstrapper that will be used to discover p2p peers.
	Bootstrapper routing.Bootstrapper

	// HandlerFunc will be called to add the registry API handler to an existing router.
	Router https.RouterFunc
}

// These values are not currently configurable
const (
	resolveRetries    = 0
	resolveTimeout    = time.Second * 5
	registryNamespace = "k8s.io"
	defaultRouterPort = "5001"
)

func init() {
	// ensure that spegel exposes metrics through the same registry used by Kubernetes components
	metrics.DefaultRegisterer = legacyregistry.Registerer()
	metrics.DefaultGatherer = legacyregistry.DefaultGatherer
}

// Start starts the embedded p2p router, and binds the registry API to an existing HTTP router.
func (c *Config) Start(ctx context.Context, nodeConfig *config.Node) error {
	localAddr := net.JoinHostPort(c.InternalAddress, c.RegistryPort)
	// distribute images for all configured mirrors. there doesn't need to be a
	// configured endpoint, just having a key for the registry will do.
	var registries, urls []string
	for host := range nodeConfig.AgentConfig.Registry.Mirrors {
		if host == localAddr {
			continue
		}
		if u, err := url.Parse("https://" + host); err != nil || docker.IsLocalhost(host) {
			logrus.Errorf("Distributed registry mirror skipping invalid registry: %s", host)
		} else {
			urls = append(urls, u.String())
			registries = append(registries, host)
		}
	}

	if len(registries) == 0 {
		logrus.Errorf("Not starting distributed registry mirror: no registries configured for distributed mirroring")
		return nil
	}

	logrus.Infof("Starting distributed registry mirror at https://%s:%s/v2 for registries %v",
		c.ExternalAddress, c.RegistryPort, registries)

	// set up the various logging logging frameworks
	level := ipfslog.LevelInfo
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		level = ipfslog.LevelDebug
		stdlog := log.New(logrus.StandardLogger().Writer(), "spegel ", log.LstdFlags)
		logger := stdr.NewWithOptions(stdlog, stdr.Options{Verbosity: ptr.To(7)})
		ctx = logr.NewContext(ctx, logger)
	}
	ipfslog.SetAllLoggers(level)

	// Get containerd client
	ociOpts := []oci.ContainerdOption{oci.WithContentPath(filepath.Join(nodeConfig.Containerd.Root, "io.containerd.content.v1.content"))}
	ociClient, err := oci.NewContainerd(nodeConfig.Containerd.Address, registryNamespace, nodeConfig.Containerd.Registry, urls, ociOpts...)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to create OCI client")
	}

	// create or load persistent private key
	keyFile := filepath.Join(nodeConfig.Containerd.Opt, "peer.key")
	keyBytes, _, err := cert.LoadOrGenerateKeyFile(keyFile, false)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to load or generate p2p private key")
	}
	privKey, err := cert.ParsePrivateKeyPEM(keyBytes)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to parse p2p private key")
	}
	p2pKey, _, err := crypto.KeyPairFromStdKey(privKey)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to convert p2p private key")
	}

	// create a peerstore to allow persisting nodes across restarts
	peerFile := filepath.Join(nodeConfig.Containerd.Opt, "peerstore.db")
	ds, err := leveldb.NewDatastore(peerFile, nil)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to create peerstore datastore")
	}
	ps, err := pstoreds.NewPeerstore(ctx, ds, pstoreds.DefaultOpts())
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to create peerstore")
	}

	// get latest tag configuration override
	if env := os.Getenv(P2pEnableLatestEnv); env != "" {
		if b, err := strconv.ParseBool(env); err != nil {
			logrus.Warnf("Invalid %s value; using default %v", P2pEnableLatestEnv, resolveLatestTag)
		} else {
			resolveLatestTag = b
		}
	}

	// get port and start p2p router
	routerPort := defaultRouterPort
	if env := os.Getenv(P2pPortEnv); env != "" {
		if i, err := strconv.Atoi(env); i == 0 || err != nil {
			logrus.Warnf("Invalid %s value; using default %v", P2pPortEnv, defaultRouterPort)
		} else {
			routerPort = env
		}
	}
	routerAddr := net.JoinHostPort(c.ExternalAddress, routerPort)

	logrus.Infof("Starting distributed registry P2P node at %s", routerAddr)
	opts := routing.WithLibP2POptions(
		libp2p.Identity(p2pKey),
		libp2p.Peerstore(ps),
		libp2p.PrivateNetwork(c.PSK),
	)
	router, err := routing.NewP2PRouter(ctx, routerAddr, c.Bootstrapper, c.RegistryPort, opts)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to create P2P router")
	}
	go router.Run(ctx)

	caCert, err := os.ReadFile(c.ServerCAFile)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to read server CA")
	}
	client := clientaccess.GetHTTPClient(caCert, c.ClientCertFile, c.ClientKeyFile)
	metrics.Register()
	registryOpts := []registry.RegistryOption{
		registry.WithResolveLatestTag(resolveLatestTag),
		registry.WithResolveRetries(resolveRetries),
		registry.WithResolveTimeout(resolveTimeout),
		registry.WithTransport(client.Transport),
		registry.WithLogger(logr.FromContextOrDiscard(ctx)),
	}
	reg, err := registry.NewRegistry(ociClient, router, registryOpts...)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to create embedded registry")
	}
	regSvr, err := reg.Server(":" + c.RegistryPort)
	if err != nil {
		return pkgerrors.WithMessage(err, "failed to create embedded registry server")
	}

	// Track images available in containerd and publish via p2p router
	go state.Track(ctx, ociClient, router, resolveLatestTag)

	mRouter, err := c.Router(ctx, nodeConfig)
	if err != nil {
		return err
	}
	mRouter.PathPrefix("/v2").Handler(regSvr.Handler)
	sRouter := mRouter.PathPrefix("/v1-{program}/p2p").Subrouter()
	sRouter.Use(auth.MaxInFlight(maxNonMutatingPeerInfoRequests, maxMutatingPeerInfoRequests))
	sRouter.Handle("", c.peerInfo())

	// Wait up to 5 seconds for the p2p network to find peers. This will return
	// immediately if the node is bootstrapping from itself.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, resolveTimeout, true, func(_ context.Context) (bool, error) {
		ready, _ := router.Ready(ctx)
		return ready, nil
	}); err != nil {
		logrus.Warnf("Failed to wait for P2P mesh to become ready, will retry in the background: %v", err)
	}
	return nil
}

// peerInfo sends a peer address retrieved from the bootstrapper via HTTP
func (c *Config) peerInfo() http.HandlerFunc {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		info, err := c.Bootstrapper.Get(req.Context())
		if err != nil {
			http.Error(resp, err.Error(), http.StatusInternalServerError)
			return
		}

		addrs := []string{}
		for _, ai := range info {
			for _, ma := range ai.Addrs {
				addrs = append(addrs, fmt.Sprintf("%s/p2p/%s", ma, ai.ID))
			}
		}

		if len(addrs) == 0 {
			http.Error(resp, "no peer addresses available", http.StatusServiceUnavailable)
			return
		}

		client, _, _ := net.SplitHostPort(req.RemoteAddr)
		if req.Header.Get("Accept") == "application/json" {
			b, err := json.Marshal(addrs)
			if err != nil {
				http.Error(resp, err.Error(), http.StatusInternalServerError)
				return
			}
			logrus.Debugf("Serving p2p peer addrs %v to client at %s", addrs, client)
			resp.Header().Set("Content-Type", "application/json")
			resp.WriteHeader(http.StatusOK)
			resp.Write(b)
			return
		}

		logrus.Debugf("Serving p2p peer addr %v to client at %s", addrs[0], client)
		resp.Header().Set("Content-Type", "text/plain")
		resp.WriteHeader(http.StatusOK)
		resp.Write([]byte(addrs[0]))
	})
}
