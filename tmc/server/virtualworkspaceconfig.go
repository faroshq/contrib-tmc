package server

import (
	"context"
	"fmt"
	"time"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog"
)

var (
	workloadAPIExportName   = "workload.kcp.io"
	schedulingAPIExportName = "scheduling.kcp.io"
	tmcLogicalCluster       = logicalcluster.NewPath("root:tmc")

	// must match values in bootstrap config
	tmcSecretNamespace = "default"
	tmcSecretName      = "tmc-controllers-token"
)

func (s *Server) configureTMCVWConfig(ctx context.Context) error {
	// wait for workloads.kcp.io to be available
	if err := wait.PollImmediateInfinite(time.Second*5, func() (bool, error) {
		klog.Infof("looking up virtual workspace URL - %s", workloadAPIExportName)
		var err error
		s.workloadsRestConfig, err = s.restConfigForAPIExport(ctx, workloadAPIExportName)
		if err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}

	// wait for scheduling.kcp.io to be available
	if err := wait.PollImmediateInfinite(time.Second*5, func() (bool, error) {
		klog.Infof("looking up virtual workspace URL - %s", schedulingAPIExportName)
		var err error
		s.schedulingRestConfig, err = s.restConfigForAPIExport(ctx, schedulingAPIExportName)
		if err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}

	return nil
}

// restConfigForAPIExport returns a *rest.Config properly configured to communicate with the endpoint for the
// APIExport's virtual workspace.
func (s *Server) restConfigForAPIExport(ctx context.Context, apiExportName string) (*rest.Config, error) {
	var apiExport *apisv1alpha1.APIExport

	if apiExportName != "" {
		var err error
		apiExport, err = s.Core.KcpClusterClient.ApisV1alpha1().Cluster(tmcLogicalCluster).APIExports().Get(ctx, apiExportName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("error getting APIExport %q: %w", apiExportName, err)
		}
	} else {
		klog.Infof("api-export-name is empty - listing")
		exports, err := s.Core.KcpClusterClient.ApisV1alpha1().Cluster(tmcLogicalCluster).APIExports().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("error listing APIExports: %w", err)
		}
		if len(exports.Items) == 0 {
			return nil, fmt.Errorf("no APIExport found")
		}
		if len(exports.Items) > 1 {
			return nil, fmt.Errorf("more than one APIExport found")
		}
		apiExport = &exports.Items[0]
	}

	//lint:ignore SA1019 Ignore the deprecation warnings
	if len(apiExport.Status.VirtualWorkspaces) < 1 {
		return nil, fmt.Errorf("APIExport %q status.virtualWorkspaces is empty", apiExportName)
	}

	// Get token from service account
	tokenSecret, err := s.Core.KubeClusterClient.CoreV1().Cluster(tmcLogicalCluster).Secrets(tmcSecretNamespace).Get(ctx, tmcSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve Secret: %w", err)
	}
	saTokenBytes := tokenSecret.Data["token"]
	if len(saTokenBytes) == 0 {
		return nil, fmt.Errorf("token secret %s/%s is missing a value for `token`", tmcSecretNamespace, tmcSecretName)
	}

	cmCAData, err := s.Core.KubeClusterClient.CoreV1().Cluster(tmcLogicalCluster).ConfigMaps(tmcSecretNamespace).Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve ConfigMap: %w", err)
	}

	// TODO: s.Core.IdentityConfig is not the right rest config to use here
	// Move to service account token
	cfg := &rest.Config{}
	cfg.BearerToken = string(saTokenBytes)
	//lint:ignore SA1019 Ignore the deprecation warnings
	cfg.Host = apiExport.Status.VirtualWorkspaces[0].URL
	cfg.CAData = []byte(cmCAData.Data["ca.crt"])
	cfg.TLSClientConfig.ServerName = "localhost"

	clusters := make(map[string]*clientcmdapi.Cluster)
	clusters["default-cluster"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
		TLSServerName:            cfg.TLSClientConfig.ServerName,
	}

	contexts := make(map[string]*clientcmdapi.Context)
	contexts["default-context"] = &clientcmdapi.Context{
		Cluster:   "default-cluster",
		Namespace: "default",
		AuthInfo:  "default",
	}

	authinfos := make(map[string]*clientcmdapi.AuthInfo)
	authinfos["default"] = &clientcmdapi.AuthInfo{
		Token: cfg.BearerToken,
	}

	clientConfig := clientcmdapi.Config{
		Kind:           "Config",
		APIVersion:     "v1",
		Clusters:       clusters,
		Contexts:       contexts,
		CurrentContext: "default-context",
		AuthInfos:      authinfos,
	}
	clientcmd.WriteToFile(clientConfig, "/tmp/tmc-rest-config.yaml")

	return cfg, nil
}
