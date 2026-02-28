/*
Copyright The Kubernetes Authors.

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

package equality

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// comparePodTemplate compares the quota-relevant fields of two PodSpecs.
// Only tolerations, container count, and resource requests/limits are compared,
// since these are the fields that affect quota and scheduling decisions.
// Other container fields (image, env, args, probes, etc.) are intentionally
// ignored to avoid unnecessary workload re-queuing when pods are mutated
// after creation (e.g., by admission webhooks).
func comparePodTemplate(a, b *corev1.PodSpec, ignoreTolerations bool) bool {
	if !ignoreTolerations && !equality.Semantic.DeepEqual(a.Tolerations, b.Tolerations) {
		return false
	}
	if !compareContainerResources(a.InitContainers, b.InitContainers) {
		return false
	}
	return compareContainerResources(a.Containers, b.Containers)
}

// compareContainerResources compares only the count and resource requests/limits
// of two container slices, ignoring all other container fields.
func compareContainerResources(a, b []corev1.Container) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equality.Semantic.DeepEqual(a[i].Resources, b[i].Resources) {
			return false
		}
	}
	return true
}

func ComparePodSets(a, b *kueue.PodSet, ignoreTolerations bool) bool {
	if a.Count != b.Count {
		return false
	}
	if ptr.Deref(a.MinCount, -1) != ptr.Deref(b.MinCount, -1) {
		return false
	}

	return comparePodTemplate(&a.Template.Spec, &b.Template.Spec, ignoreTolerations)
}

func ComparePodSetSlices(a, b []kueue.PodSet, ignoreTolerations bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !ComparePodSets(&a[i], &b[i], ignoreTolerations) {
			return false
		}
	}
	return true
}
