/*
Copyright © contributors to CloudNativePG, established as
CloudNativePG a Series of LF Projects, LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

SPDX-License-Identifier: Apache-2.0
*/

package controller

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"
)

var (
	isUsefulConfigMap = func(object client.Object) bool {
		return isOwnedByClusterOrSatisfiesPredicate(object, func(object client.Object) bool {
			_, ok := object.(*corev1.ConfigMap)
			return ok && hasReloadLabelSet(object)
		})
	}

	isUsefulClusterSecret = func(object client.Object) bool {
		return isOwnedByClusterOrSatisfiesPredicate(object, func(object client.Object) bool {
			_, ok := object.(*corev1.Secret)
			return ok && hasReloadLabelSet(object)
		})
	}

	configMapsPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isUsefulConfigMap(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isUsefulConfigMap(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isUsefulConfigMap(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isUsefulConfigMap(e.ObjectNew)
		},
	}

	secretsPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isUsefulClusterSecret(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isUsefulClusterSecret(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isUsefulClusterSecret(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isUsefulClusterSecret(e.ObjectNew)
		},
	}
)

func (r *ClusterReconciler) nodesPredicate() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, oldOk := e.ObjectOld.(*corev1.Node)
			newNode, newOk := e.ObjectNew.(*corev1.Node)
			if !oldOk || !newOk {
				return false
			}

			if oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable {
				return true
			}

			// check if any of the watched drain taints have changed.
			for _, taint := range r.drainTaints {
				oldTaintIndex := slices.IndexFunc(oldNode.Spec.Taints, func(t corev1.Taint) bool { return t.Key == taint })
				newTaintIndex := slices.IndexFunc(newNode.Spec.Taints, func(t corev1.Taint) bool { return t.Key == taint })

				switch {
				case oldTaintIndex == -1 && newTaintIndex == -1:
					continue
				case oldTaintIndex == -1 || newTaintIndex == -1:
					return true
				}

				// exists in both - check if value or effect is different
				oldTaint := oldNode.Spec.Taints[oldTaintIndex]
				newTaint := newNode.Spec.Taints[newTaintIndex]
				if oldTaint.Value != newTaint.Value || oldTaint.Effect != newTaint.Effect {
					return true
				}
			}

			return false
		},
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}

func isOwnedByClusterOrSatisfiesPredicate(
	object client.Object,
	predicate func(client.Object) bool,
) bool {
	_, owned := IsOwnedByCluster(object)
	return owned || predicate(object)
}

func hasReloadLabelSet(obj client.Object) bool {
	_, hasLabel := obj.GetLabels()[utils.WatchedLabelName]
	return hasLabel
}
