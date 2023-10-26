/*
Copyright 2023 The Kubernetes Authors.

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

package shardlease

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/pointer"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coordinformers "k8s.io/client-go/informers/coordination/v1"
	clientset "k8s.io/client-go/kubernetes"
	coordlisters "k8s.io/client-go/listers/coordination/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-helpers/sharding"
	"k8s.io/component-helpers/sharding/leases"
)

// Controller sets ttl annotations on nodes, based on cluster size.
type Controller struct {
	kubeClient clientset.Interface

	leaseLister coordlisters.LeaseLister

	leaseQueue workqueue.RateLimitingInterface

	hasSynced func() bool

	clock clock.PassiveClock
}

// New creates a new Controller.
func New(ctx context.Context, kubeClient clientset.Interface, leaseInformer coordinformers.LeaseInformer) *Controller {
	logger := klog.FromContext(ctx)

	c := &Controller{
		kubeClient:  kubeClient,
		leaseLister: leaseInformer.Lister(),
		leaseQueue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{
			Name: "shardleasecontroller",
		}),
		clock: clock.RealClock{},
	}

	_, _ = leaseInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.addLease(logger, obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			c.updateLease(logger, old, newObj)
		},
	})

	c.hasSynced = leaseInformer.Informer().HasSynced

	return c
}

// Run begins watching and syncing.
func (c *Controller) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.leaseQueue.ShutDown()

	logger := klog.FromContext(ctx)
	logger.Info("Starting shardlease controller")
	defer logger.Info("Shutting down shardlease controller")

	if !cache.WaitForNamedCacheSync("shardlease", ctx.Done(), c.hasSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.worker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) addLease(logger klog.Logger, obj interface{}) {
	lease, ok := obj.(*coordinationv1.Lease)
	if !ok {
		logger.Error(fmt.Errorf("unexpected object type %T", obj), "")
		return
	}

	if !isShardLease(lease) {
		return
	}

	c.enqueueLease(logger, lease)
}

func (c *Controller) updateLease(logger klog.Logger, oldObj, newObj interface{}) {
	lease, ok := newObj.(*coordinationv1.Lease)
	if !ok {
		logger.Error(fmt.Errorf("unexpected object type %T", newObj), "")
		return
	}
	oldLease, ok := oldObj.(*coordinationv1.Lease)
	if !ok {
		logger.Error(fmt.Errorf("unexpected object type %T", oldObj), "")
		return
	}

	if isShardLease(oldLease) != isShardLease(lease) {
		c.enqueueLease(logger, lease)
		return
	}

	if leases.ToState(oldLease, c.clock) != leases.ToState(lease, c.clock) {
		c.enqueueLease(logger, lease)
	}
}

func isShardLease(lease *coordinationv1.Lease) bool {
	return lease.Labels[sharding.LabelRing] != ""
}

func (c *Controller) enqueueLease(logger klog.Logger, lease *coordinationv1.Lease) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(lease)
	if err != nil {
		logger.Error(nil, "couldn't get key for object", "object", klog.KObj(lease))
		return
	}

	c.leaseQueue.Add(key)
}

func (c *Controller) worker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	logger := klog.FromContext(ctx)

	key, quit := c.leaseQueue.Get()
	if quit {
		return false
	}
	defer c.leaseQueue.Done(key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key.(string))
	if err != nil {
		logger.Error(err, "Failed to split meta namespace cache key", "cacheKey", key)
		return true
	}

	leaseLogger := klog.LoggerWithValues(logger, "lease", klog.KRef(namespace, name))
	requeueAfter, err := c.syncLease(klog.NewContext(ctx, leaseLogger), namespace, name)
	switch {
	case err != nil:
		c.leaseQueue.AddRateLimited(key)
		leaseLogger.V(2).Info("Error syncing shard lease", "err", err)
	case requeueAfter > 0:
		c.leaseQueue.Forget(key)
		c.leaseQueue.AddAfter(key, requeueAfter)
	default:
		c.leaseQueue.Forget(key)
	}

	return true
}

func (c *Controller) syncLease(ctx context.Context, namespace, name string) (time.Duration, error) {
	log := klog.FromContext(ctx)

	lease, err := c.leaseLister.Leases(namespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Object is gone, stop reconciling")
			return 0, nil
		}
		return 0, fmt.Errorf("error retrieving object from store: %w", err)
	}

	if lease.Labels[sharding.LabelRing] == "" {
		log.V(1).Info("Ignoring non-shard lease")
		return 0, nil
	}

	var (
		previousState = leases.StateFromString(lease.Labels[sharding.LabelState])
		shard         = leases.ToShard(lease, c.clock)
	)
	log = klog.LoggerWithValues(log, "state", shard.State, "expirationTime", shard.Times.Expiration.UTC(), "leaseDuration", shard.Times.LeaseDuration)

	// maintain state label
	if previousState != shard.State {
		metav1.SetMetaDataLabel(&lease.ObjectMeta, sharding.LabelState, shard.State.String())

		lease, err = c.kubeClient.CoordinationV1().Leases(namespace).Update(ctx, lease, metav1.UpdateOptions{})
		if err != nil {
			return 0, fmt.Errorf("failed to update state label on lease: %w", err)
		}
	}

	// act on state and determine when to check again
	var requeueAfter time.Duration
	switch shard.State {
	case leases.Ready:
		if previousState != leases.Ready {
			log.Info("Shard got ready")
		}
		requeueAfter = shard.Times.ToExpired
	case leases.Expired:
		log.Info("Shard lease has expired")
		requeueAfter = shard.Times.ToUncertain
	case leases.Uncertain:
		log.Info("Shard lease has expired more than leaseDuration ago, trying to acquire shard lease")

		now := metav1.NewMicroTime(c.clock.Now())
		transitions := int32(0)
		if lease.Spec.LeaseTransitions != nil {
			transitions = *lease.Spec.LeaseTransitions
		}

		lease.Spec.HolderIdentity = pointer.String(sharding.IdentityShardLeaseController)
		lease.Spec.LeaseDurationSeconds = pointer.Int32(2 * int32(shard.Times.LeaseDuration.Round(time.Second).Seconds()))
		lease.Spec.AcquireTime = &now
		lease.Spec.RenewTime = &now
		lease.Spec.LeaseTransitions = pointer.Int32(transitions + 1)

		if _, err = c.kubeClient.CoordinationV1().Leases(namespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
			return 0, fmt.Errorf("error acquiring shard lease: %w", err)
		}

		// lease will be enqueued once we observe our previous update via watch
		// requeue with leaseDuration just to be sure
		requeueAfter = shard.Times.LeaseDuration
	case leases.Dead:
		// garbage collect later
		requeueAfter = shard.Times.ToOrphaned
	case leases.Orphaned:
		// garbage collect and forget orphaned leases
		return 0, c.kubeClient.CoordinationV1().Leases(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	default:
		// Unknown, forget lease
		return 0, nil
	}

	return requeueAfter, nil
}
