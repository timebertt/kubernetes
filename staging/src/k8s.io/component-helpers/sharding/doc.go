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

// Package sharding contains constants and helpers related to the sharding feature.
// It is implemented in k8s.io/component-helpers for simplicity, although it might not be the correct place for it
// long-term. Placing it here allows importing it in k8s.io/apiserver and kube-controller-manager.
// With this, different approaches for which component is responsible for assigning objects to shards can be implemented
// without moving packages around.
package sharding
