/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package synctarget

import (
	"context"
	"net/url"
	"os"
	"path"

	"github.com/davecgh/go-spew/spew"
	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/contrib-tmc/apis/workload/v1alpha1"
	syncerbuilder "github.com/kcp-dev/contrib-tmc/tmc/virtual/syncer/builder"
)

func (c *Controller) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget, workspaceShards []*corev1alpha1.Shard) (*workloadv1alpha1.SyncTarget, error) {
	logger := klog.FromContext(ctx)
	syncTargetCopy := syncTarget.DeepCopy()

	spew.Dump(syncTargetCopy)
	os.Exit(1)

	labels := syncTargetCopy.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[workloadv1alpha1.InternalSyncTargetKeyLabel] = workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTargetCopy), syncTargetCopy.Name)
	syncTargetCopy.SetLabels(labels)

	desiredVWURLs := map[string]workloadv1alpha1.VirtualWorkspace{}
	desiredTunnelWorkspaceURLs := map[string]workloadv1alpha1.TunnelWorkspace{}
	syncTargetClusterName := logicalcluster.From(syncTarget)

	var rootShardKey string
	for _, workspaceShard := range workspaceShards {
		if workspaceShard.Spec.VirtualWorkspaceURL != "" {
			shardVWURL, err := url.Parse(workspaceShard.Spec.VirtualWorkspaceURL)
			if err != nil {
				logger.Error(err, "failed to parse workspaceShard.Spec.VirtualWorkspaceURL")
				return nil, err
			}

			syncerVirtualWorkspaceURL := *shardVWURL
			syncerVirtualWorkspaceURL.Path = path.Join(
				shardVWURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				syncerbuilder.SyncerVirtualWorkspaceName,
				logicalcluster.From(syncTargetCopy).String(),
				syncTargetCopy.Name,
				string(syncTargetCopy.UID),
			)

			upsyncerVirtualWorkspaceURL := *shardVWURL
			(&upsyncerVirtualWorkspaceURL).Path = path.Join(
				shardVWURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				syncerbuilder.UpsyncerVirtualWorkspaceName,
				logicalcluster.From(syncTargetCopy).String(),
				syncTargetCopy.Name,
				string(syncTargetCopy.UID),
			)

			syncerURL := (&syncerVirtualWorkspaceURL).String()
			upsyncerURL := (&upsyncerVirtualWorkspaceURL).String()

			if workspaceShard.Name == corev1alpha1.RootShard {
				rootShardKey = shardVWURL.String()
			}
			desiredVWURLs[shardVWURL.String()] = workloadv1alpha1.VirtualWorkspace{
				SyncerURL:   syncerURL,
				UpsyncerURL: upsyncerURL,
			}

			tunnelWorkspaceURL, err := url.JoinPath(workspaceShard.Spec.BaseURL, syncTargetClusterName.Path().RequestPath())
			if err != nil {
				return nil, err
			}
			desiredTunnelWorkspaceURLs[shardVWURL.String()] = workloadv1alpha1.TunnelWorkspace{
				URL: tunnelWorkspaceURL,
			}
		}
	}

	// Let's always add the desired URLs in the same order:
	// - urls for the root shard will always be added at the first place,
	//   in order to ensure compatibility with the shard-unaware Syncer
	// - urls for other shards which will be added in the lexical order of the
	// corresponding shard URLs.
	var desiredVirtualWorkspaces []workloadv1alpha1.VirtualWorkspace //nolint:prealloc
	if rootShardVirtualWorkspace, ok := desiredVWURLs[rootShardKey]; ok {
		desiredVirtualWorkspaces = append(desiredVirtualWorkspaces, rootShardVirtualWorkspace)
		delete(desiredVWURLs, rootShardKey)
	}
	for _, shardURL := range sets.StringKeySet(desiredVWURLs).List() {
		desiredVirtualWorkspaces = append(desiredVirtualWorkspaces, desiredVWURLs[shardURL])
	}
	var desiredTunnelWorkspaces []workloadv1alpha1.TunnelWorkspace //nolint:prealloc
	if rootShardTunnelWorkspace, ok := desiredTunnelWorkspaceURLs[rootShardKey]; ok {
		desiredTunnelWorkspaces = append(desiredTunnelWorkspaces, rootShardTunnelWorkspace)
		delete(desiredTunnelWorkspaceURLs, rootShardKey)
	}
	for _, shardURL := range sets.StringKeySet(desiredTunnelWorkspaceURLs).List() {
		desiredTunnelWorkspaces = append(desiredTunnelWorkspaces, desiredTunnelWorkspaceURLs[shardURL])
	}

	syncTargetCopy.Status.VirtualWorkspaces = desiredVirtualWorkspaces
	syncTargetCopy.Status.TunnelWorkspaces = desiredTunnelWorkspaces
	return syncTargetCopy, nil
}
