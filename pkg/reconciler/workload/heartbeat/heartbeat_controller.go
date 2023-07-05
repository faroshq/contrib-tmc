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

package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/kcp/pkg/logging"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/contrib-tmc/apis/workload/v1alpha1"
	tmcclientset "github.com/kcp-dev/contrib-tmc/client/clientset/versioned/cluster"
	workloadv1alpha1informers "github.com/kcp-dev/contrib-tmc/client/informers/externalversions/workload/v1alpha1"
)

const ControllerName = "tmc-synctarget-heartbeat"

type Controller struct {
	queue              workqueue.RateLimitingInterface
	kcpClusterClient   kcpclientset.ClusterInterface
	tmcClusterClient   tmcclientset.ClusterInterface
	heartbeatThreshold time.Duration
	getSyncTarget      func(clusterName logicalcluster.Name, name string) (*workloadv1alpha1.SyncTarget, error)
}

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	tmcClusterClient tmcclientset.ClusterInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	heartbeatThreshold time.Duration,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:              queue,
		kcpClusterClient:   kcpClusterClient,
		tmcClusterClient:   tmcClusterClient,
		heartbeatThreshold: heartbeatThreshold,
		getSyncTarget: func(clusterName logicalcluster.Name, name string) (*workloadv1alpha1.SyncTarget, error) {
			return syncTargetInformer.Cluster(clusterName).Lister().Get(name)
		},
	}

	_, _ = syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing SyncTarget")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "error parsing key")
		return nil
	}

	current, err := c.getSyncTarget(clusterName, name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get SyncTarget from lister", "cluster", clusterName, "name", name)
		}

		return nil
	}

	previous := current
	current = current.DeepCopy()

	logger = logging.WithObject(logger, previous)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, key, current); err != nil {
		errs = append(errs, err)
	}

	if err := c.patchIfNeeded(ctx, previous, current); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

func (c *Controller) patchIfNeeded(ctx context.Context, old, obj *workloadv1alpha1.SyncTarget) error {
	specOrObjectMetaChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec) || !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

	if specOrObjectMetaChanged && statusChanged {
		panic("Programmer error: spec and status changed in same reconcile iteration")
	}

	if !specOrObjectMetaChanged && !statusChanged {
		return nil
	}

	clusterSyncTargetForPatch := func(synctarget *workloadv1alpha1.SyncTarget) workloadv1alpha1.SyncTarget {
		var ret workloadv1alpha1.SyncTarget
		if specOrObjectMetaChanged {
			ret.ObjectMeta = synctarget.ObjectMeta
			ret.Spec = synctarget.Spec
		} else {
			ret.Status = synctarget.Status
		}
		return ret
	}

	clusterName := logicalcluster.From(old)
	name := old.Name

	oldForPatch := clusterSyncTargetForPatch(old)
	// to ensure they appear in the patch as preconditions
	oldForPatch.UID = ""
	oldForPatch.ResourceVersion = ""

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for SyncTarget %s|%s: %w", clusterName, name, err)
	}

	newForPatch := clusterSyncTargetForPatch(obj)
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for SyncTarget %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for SyncTarget %s|%s: %w", clusterName, name, err)
	}

	// TODO: Check if status changes and patch only status.
	// https://github.com/kcp-dev/contrib-tmc/issues/1

	_, err = c.tmcClusterClient.Cluster(clusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch SyncTarget %s|%s: %w", clusterName, name, err)

	}
	return err
}
