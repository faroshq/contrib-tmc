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

package placement

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	corev1listers "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/contrib-tmc/apis/scheduling/v1alpha1"
	tmcclientset "github.com/kcp-dev/contrib-tmc/client/clientset/versioned/cluster"
	schedulingv1alpha1informers "github.com/kcp-dev/contrib-tmc/client/informers/externalversions/scheduling/v1alpha1"
	schedulingv1alpha1listers "github.com/kcp-dev/contrib-tmc/client/listers/scheduling/v1alpha1"
)

const (
	ControllerName      = "tmc-scheduling-placement"
	byLocationWorkspace = ControllerName + "-byLocationWorkspace"
)

// NewController returns a new controller placing namespaces onto locations by create
// a placement annotation..
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	tmcClusterClient tmcclientset.ClusterInterface,
	namespaceInformer kcpcorev1informers.NamespaceClusterInformer,
	locationInformer schedulingv1alpha1informers.LocationClusterInformer,
	placementInformer schedulingv1alpha1informers.PlacementClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(ns *corev1.Namespace, duration time.Duration) {
			key, err := kcpcache.MetaClusterNamespaceKeyFunc(ns)
			if err != nil {
				runtime.HandleError(err)
				return
			}
			queue.AddAfter(key, duration)
		},
		kcpClusterClient: kcpClusterClient,
		tmcClusterClient: tmcClusterClient,

		namespaceLister: namespaceInformer.Lister(),

		listLocationsByPath: func(path logicalcluster.Path) ([]*schedulingv1alpha1.Location, error) {
			cluster, exists := path.Name()
			if !exists {
				return nil, fmt.Errorf("cluster name not found in path %q", path)
			}
			return locationInformer.Lister().Cluster(cluster).List(labels.Everything())
		},

		placementLister:  placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),
	}

	if err := placementInformer.Informer().AddIndexers(cache.Indexers{
		byLocationWorkspace: indexByLocationWorkspace,
	}); err != nil {
		return nil, err
	}

	indexers.AddIfNotPresentOrDie(locationInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPath: indexers.IndexByLogicalClusterPath,
	})

	// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
	var namespaceBlocklist = sets.New[string]("kube-system", "kube-public", "kube-node-lease")
	_, _ = namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch ns := obj.(type) {
			case *corev1.Namespace:
				return !namespaceBlocklist.Has(ns.Name)
			case cache.DeletedFinalStateUnknown:
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: c.enqueueNamespace,
			UpdateFunc: func(old, obj interface{}) {
				oldNs := old.(*corev1.Namespace)
				newNs := obj.(*corev1.Namespace)

				if !reflect.DeepEqual(oldNs.Annotations, newNs.Annotations) {
					c.enqueueNamespace(obj)
				}
			},
			DeleteFunc: c.enqueueNamespace,
		},
	})

	_, _ = locationInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: c.enqueueLocation,
			UpdateFunc: func(old, obj interface{}) {
				oldLoc := old.(*schedulingv1alpha1.Location)
				newLoc := obj.(*schedulingv1alpha1.Location)
				if !reflect.DeepEqual(oldLoc.Spec, newLoc.Spec) || !reflect.DeepEqual(oldLoc.Labels, newLoc.Labels) {
					c.enqueueLocation(obj)
				}
			},
			DeleteFunc: c.enqueueLocation,
		},
	)

	_, _ = placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueuePlacement,
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj) },
		DeleteFunc: c.enqueuePlacement,
	})

	return c, nil
}

// controller.
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*corev1.Namespace, time.Duration)

	kcpClusterClient kcpclientset.ClusterInterface
	tmcClusterClient tmcclientset.ClusterInterface

	namespaceLister corev1listers.NamespaceClusterLister

	listLocationsByPath func(path logicalcluster.Path) ([]*schedulingv1alpha1.Location, error)

	placementLister  schedulingv1alpha1listers.PlacementClusterLister
	placementIndexer cache.Indexer
}

func (c *controller) enqueuePlacement(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing Placement")
	c.queue.Add(key)
}

// enqueueNamespace enqueues all placements for the namespace.
func (c *controller) enqueueNamespace(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	placements, err := c.placementLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, placement := range placements {
		namespaceKey := key
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(placement)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing Placement because Namespace changed", "Namespace", namespaceKey)
		c.queue.Add(key)
	}
}

func (c *controller) enqueueLocation(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}

	location, ok := obj.(*schedulingv1alpha1.Location)
	if !ok {
		runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
		return
	}

	// placements referencing by cluster name
	placements, err := c.placementIndexer.ByIndex(byLocationWorkspace, logicalcluster.From(location).String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if path := location.Annotations[core.LogicalClusterPathAnnotationKey]; path != "" {
		// placements referencing by path
		placementsByPath, err := c.placementIndexer.ByIndex(byLocationWorkspace, path)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		placements = append(placements, placementsByPath...)
	}

	for _, obj := range placements {
		placement := obj.(*schedulingv1alpha1.Placement)
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(placement)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing Placement because Location changed")
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

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

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.placementLister.Cluster(clusterName).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	if err := c.patchIfNeeded(ctx, old, obj); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

func (c *controller) patchIfNeeded(ctx context.Context, old, obj *schedulingv1alpha1.Placement) error {
	specOrObjectMetaChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec) || !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

	if specOrObjectMetaChanged && statusChanged {
		panic("Programmer error: spec and status changed in same reconcile iteration")
	}

	if !specOrObjectMetaChanged && !statusChanged {
		return nil
	}

	clusterPlacementForPatch := func(placement *schedulingv1alpha1.Placement) schedulingv1alpha1.Placement {
		var ret schedulingv1alpha1.Placement
		if specOrObjectMetaChanged {
			ret.ObjectMeta = placement.ObjectMeta
			ret.Spec = placement.Spec
		} else {
			ret.Status = placement.Status
		}
		return ret
	}

	clusterName := logicalcluster.From(old)
	name := old.Name

	oldForPatch := clusterPlacementForPatch(old)
	// to ensure they appear in the patch as preconditions
	oldForPatch.UID = ""
	oldForPatch.ResourceVersion = ""

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for Placement %s|%s: %w", clusterName, name, err)
	}

	newForPatch := clusterPlacementForPatch(obj)
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for Placement %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for Placement %s|%s: %w", clusterName, name, err)
	}

	// TODO: Check if status changes and patch only status.
	// https://github.com/kcp-dev/contrib-tmc/issues/1

	_, err = c.tmcClusterClient.Cluster(clusterName.Path()).SchedulingV1alpha1().Placements().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch Placement %s|%s: %w", clusterName, name, err)

	}
	return err
}
