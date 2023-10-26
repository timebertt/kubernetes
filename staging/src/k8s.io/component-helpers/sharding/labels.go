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

package sharding

const (
	// LabelRing is the label on Lease objects that identifies the ring that the shard belongs to.
	LabelRing = "sharding.alpha.kubernetes.io/ring"
	// LabelState is the label on Lease objects that reflects the state of a shard for observability purposes.
	// This label is maintained by the shardlease controller.
	LabelState = "sharding.alpha.kubernetes.io/state"
	// IdentityShardLeaseController is the identity that the shardlease controller uses to acquire leases of unavailable
	// shards.
	IdentityShardLeaseController = "shardlease-controller"
)
