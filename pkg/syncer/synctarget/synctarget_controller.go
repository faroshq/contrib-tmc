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
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/go-logr/logr"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/contrib-tmc/apis/workload/v1alpha1"
	workloadv1alpha1client "github.com/kcp-dev/contrib-tmc/client/clientset/versioned/typed/workload/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/contrib-tmc/client/informers/externalversions/workload/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/contrib-tmc/client/listers/workload/v1alpha1"
)

const (
	resyncPeriod   = 10 * time.Hour
	controllerName = "kcp-syncer-synctarget-gvrsource-controller"
)

type controller struct {
	queue workqueue.RateLimitingInterface

	syncTargetUID    types.UID
	syncTargetLister workloadv1alpha1listers.SyncTargetLister
	syncTargetClient workloadv1alpha1client.SyncTargetInterface

	reconcilers []reconciler
}

// NewSyncTargetController returns a controller that watches the [workloadv1alpha1.SyncTarget]
// associated to this syncer.
// It then calls the update methods on the shardManager and gvrSource
// that were passed in arguments, to update available shards and GVRs
// according to the content of the SyncTarget status.
func NewSyncTargetController(
	syncerLogger logr.Logger,
	syncTargetClient workloadv1alpha1client.SyncTargetInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetInformer,
	syncTargetName string,
	syncTargetClusterName logicalcluster.Name,
	syncTargetUID types.UID,
	gvrSource *syncTargetGVRSource,
	shardManager *shardManager,
	startShardTunneler func(ctx context.Context, shardURL workloadv1alpha1.TunnelWorkspace),
) (*controller, error) {
	c := &controller{
		queue:            workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		syncTargetUID:    syncTargetUID,
		syncTargetLister: syncTargetInformer.Lister(),
		syncTargetClient: syncTargetClient,

		reconcilers: []reconciler{
			gvrSource,
			shardManager,
			&tunnelerReconciler{
				startedTunnelers:   make(map[workloadv1alpha1.TunnelWorkspace]tunnelerStopper),
				startShardTunneler: startShardTunneler,
			},
		},
	}

	logger := logging.WithReconciler(syncerLogger, controllerName)

	_, _ = syncTargetInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			_, name, err := cache.SplitMetaNamespaceKey(key)
			if err != nil {
				return false
			}
			return name == syncTargetName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(obj, logger) },
			UpdateFunc: func(old, obj interface{}) { c.enqueue(obj, logger) },
			DeleteFunc: func(obj interface{}) { c.enqueue(obj, logger) },
		},
	})

	return c, nil
}

func (c *controller) enqueue(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing SyncTarget")

	c.queue.Add(key)
}

// Start starts the controller worker.
func (c *controller) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
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

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) (bool, error) {
	logger := klog.FromContext(ctx)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return false, nil
	}

	syncTarget, err := c.syncTargetLister.Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if apierrors.IsNotFound(err) || syncTarget.GetUID() != c.syncTargetUID {
		return c.reconcile(ctx, nil)
	}

	previous := syncTarget
	syncTarget = syncTarget.DeepCopy()

	var errs []error
	requeue, err := c.reconcile(ctx, syncTarget)
	if err != nil {
		errs = append(errs, err)
	}

	if err := c.patchIfNeeded(ctx, previous, syncTarget); err != nil {
		errs = append(errs, err)
	}

	return requeue, errors.NewAggregate(errs)
}

func (c *controller) patchIfNeeded(ctx context.Context, old, obj *workloadv1alpha1.SyncTarget) error {
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

	_, err = c.syncTargetClient.Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch SyncTarget %s|%s: %w", clusterName, name, err)

	}
	return err
}
