package server

import (
	"context"
	"fmt"
	"time"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	tmcclientset "github.com/kcp-dev/contrib-tmc/client/clientset/versioned/cluster"
	tmcclusterclientset "github.com/kcp-dev/contrib-tmc/client/clientset/versioned/cluster"
	tmcinformers "github.com/kcp-dev/contrib-tmc/client/informers/externalversions"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	"github.com/kcp-dev/logicalcluster/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	tmcExportName = "tmc.kcp.io"
	tmcCluster    = "root:tmc"
)

func (s *Server) setupKCPAdminRestConfig(ctx context.Context) (err error) {
	if len(s.Core.Options.Extra.RootShardKubeconfigFile) > 0 {
		s.RestKCPAdmin, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: s.Core.Options.Extra.RootShardKubeconfigFile}, &clientcmd.ConfigOverrides{CurrentContext: "system:admin"}).ClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load the kubeconfig from: %s, for the root shard, err: %w", s.Core.Options.Extra.RootShardKubeconfigFile, err)
		}
	} else {
		s.RestKCPAdmin, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: ".tmc/admin.kubeconfig"}, &clientcmd.ConfigOverrides{CurrentContext: "system:admin"}).ClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load the kubeconfig from: %s, for the root shard, err: %w", ".tmc/admin.kubeconfig", err)
		}
	}

	return
}

func (s *Server) setupRestConfig(ctx context.Context) error {
	clusterRest := s.Core.IdentityConfig
	// bootstrap rest config for controllers
	if err := wait.PollImmediateInfinite(time.Second*5, func() (bool, error) {
		klog.Infof("looking up virtual workspace URL - %s", tmcExportName)
		var err error
		s.RestVM, err = restConfigForAPIExport(ctx, clusterRest, tmcExportName)
		if err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) setupClusterAwareClient(ctx context.Context) (err error) {
	s.TmcClusterClient, err = tmcclusterclientset.NewForConfig(s.RestVM)
	if err != nil {
		return err
	}

	informerConfigTMC := rest.CopyConfig(s.RestVM)
	informerConfigTMC.UserAgent = "tmc-informers"
	informerTmcClient, err := tmcclientset.NewForConfig(informerConfigTMC)
	if err != nil {
		return err
	}

	s.TmcSharedInformerFactory = tmcinformers.NewSharedInformerFactoryWithOptions(
		informerTmcClient,
		resyncPeriod,
	)

	informerConfigKcp := rest.CopyConfig(s.Core.IdentityConfig)
	informerConfigKcp.UserAgent = "kcp-informers"

	informerKcpClient, err := kcpclientset.NewForConfig(informerConfigKcp)
	if err != nil {
		return err
	}

	cacheClientConfig, err := s.Core.Options.Cache.Client.RestConfig(rest.CopyConfig(s.Core.GenericConfig.LoopbackClientConfig))
	if err != nil {
		return err
	}
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheClientConfig)
	if err != nil {
		return err
	}
	cacheKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cacheClientConfig)
	if err != nil {
		return err
	}

	s.CacheKubeSharedInformerFactory = kcpkubernetesinformers.NewSharedInformerFactoryWithOptions(
		cacheKubeClusterClient,
		resyncPeriod,
	)

	s.CacheKcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
		cacheKcpClusterClient,
		resyncPeriod,
	)
	s.KcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
		informerKcpClient,
		resyncPeriod,
	)

	clientgoExternalClient, err := kcpkubernetesclientset.NewForConfig(s.Core.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	s.KubeSharedInformerFactory = kcpkubernetesinformers.NewSharedInformerFactory(clientgoExternalClient, 10*time.Minute)

	return
}

// restConfigForAPIExport returns a *rest.Config properly configured to communicate with the endpoint for the
// APIExport's virtual workspace.
func restConfigForAPIExport(ctx context.Context, cfg *rest.Config, apiExportName string) (*rest.Config, error) {
	scheme := runtime.NewScheme()
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding apis.kcp.dev/v1alpha1 to scheme: %w", err)
	}

	client, err := kcpclientset.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("error creating APIExport client: %w", err)
	}

	tmcClient := client.Cluster(logicalcluster.NewPath(tmcCluster))

	apiExport, err := tmcClient.ApisV1alpha1().APIExports().Get(ctx, apiExportName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error creating APIExport client: %w", err)
	}

	//lint:ignore SA1019 Ignore the deprecation warnings
	if len(apiExport.Status.VirtualWorkspaces) < 1 {
		return nil, fmt.Errorf("APIExport %q status.virtualWorkspaces is empty", apiExportName)
	}

	cfg = rest.CopyConfig(cfg)
	//lint:ignore SA1019 Ignore the deprecation warnings
	cfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	return cfg, nil
}
