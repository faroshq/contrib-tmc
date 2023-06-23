//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright The KCP Authors.

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

// Code generated by kcp code-generator. DO NOT EDIT.

package v1alpha1

import (
	"context"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpinformers "github.com/kcp-dev/apimachinery/v2/third_party/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	schedulingv1alpha1 "github.com/kcp-dev/contrib-tmc/apis/scheduling/v1alpha1"
	scopedclientset "github.com/kcp-dev/contrib-tmc/client/clientset/versioned"
	clientset "github.com/kcp-dev/contrib-tmc/client/clientset/versioned/cluster"
	"github.com/kcp-dev/contrib-tmc/client/informers/externalversions/internalinterfaces"
	schedulingv1alpha1listers "github.com/kcp-dev/contrib-tmc/client/listers/scheduling/v1alpha1"
)

// PlacementClusterInformer provides access to a shared informer and lister for
// Placements.
type PlacementClusterInformer interface {
	Cluster(logicalcluster.Name) PlacementInformer
	Informer() kcpcache.ScopeableSharedIndexInformer
	Lister() schedulingv1alpha1listers.PlacementClusterLister
}

type placementClusterInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewPlacementClusterInformer constructs a new informer for Placement type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewPlacementClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredPlacementClusterInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredPlacementClusterInformer constructs a new informer for Placement type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredPlacementClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) kcpcache.ScopeableSharedIndexInformer {
	return kcpinformers.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SchedulingV1alpha1().Placements().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SchedulingV1alpha1().Placements().Watch(context.TODO(), options)
			},
		},
		&schedulingv1alpha1.Placement{},
		resyncPeriod,
		indexers,
	)
}

func (f *placementClusterInformer) defaultInformer(client clientset.ClusterInterface, resyncPeriod time.Duration) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredPlacementClusterInformer(client, resyncPeriod, cache.Indexers{
		kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc,
	},
		f.tweakListOptions,
	)
}

func (f *placementClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return f.factory.InformerFor(&schedulingv1alpha1.Placement{}, f.defaultInformer)
}

func (f *placementClusterInformer) Lister() schedulingv1alpha1listers.PlacementClusterLister {
	return schedulingv1alpha1listers.NewPlacementClusterLister(f.Informer().GetIndexer())
}

// PlacementInformer provides access to a shared informer and lister for
// Placements.
type PlacementInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() schedulingv1alpha1listers.PlacementLister
}

func (f *placementClusterInformer) Cluster(clusterName logicalcluster.Name) PlacementInformer {
	return &placementInformer{
		informer: f.Informer().Cluster(clusterName),
		lister:   f.Lister().Cluster(clusterName),
	}
}

type placementInformer struct {
	informer cache.SharedIndexInformer
	lister   schedulingv1alpha1listers.PlacementLister
}

func (f *placementInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

func (f *placementInformer) Lister() schedulingv1alpha1listers.PlacementLister {
	return f.lister
}

type placementScopedInformer struct {
	factory          internalinterfaces.SharedScopedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

func (f *placementScopedInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&schedulingv1alpha1.Placement{}, f.defaultInformer)
}

func (f *placementScopedInformer) Lister() schedulingv1alpha1listers.PlacementLister {
	return schedulingv1alpha1listers.NewPlacementLister(f.Informer().GetIndexer())
}

// NewPlacementInformer constructs a new informer for Placement type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewPlacementInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredPlacementInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredPlacementInformer constructs a new informer for Placement type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredPlacementInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SchedulingV1alpha1().Placements().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SchedulingV1alpha1().Placements().Watch(context.TODO(), options)
			},
		},
		&schedulingv1alpha1.Placement{},
		resyncPeriod,
		indexers,
	)
}

func (f *placementScopedInformer) defaultInformer(client scopedclientset.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredPlacementInformer(client, resyncPeriod, cache.Indexers{}, f.tweakListOptions)
}
