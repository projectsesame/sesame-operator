// Copyright Project Contour Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sesame

import (
	"context"
	"fmt"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/pkg/slice"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var finalizer = operatorv1alpha1.SesameFinalizer

// EnsureFinalizer ensures the finalizer is added to Sesame.
func EnsureFinalizer(ctx context.Context, cli client.Client, sesame *operatorv1alpha1.Sesame) error {
	if !slice.ContainsString(sesame.Finalizers, finalizer) {
		updated := sesame.DeepCopy()
		updated.Finalizers = append(updated.Finalizers, finalizer)
		if err := cli.Update(ctx, updated); err != nil {
			return fmt.Errorf("failed to add finalizer %s to sesame %s/%s: %w",
				finalizer, sesame.Namespace, sesame.Name, err)
		}
	}
	return nil
}

// EnsureFinalizerRemoved ensures the finalizer is removed from Sesame.
func EnsureFinalizerRemoved(ctx context.Context, cli client.Client, sesame *operatorv1alpha1.Sesame) error {
	if slice.ContainsString(sesame.Finalizers, finalizer) {
		updated := sesame.DeepCopy()
		updated.Finalizers = slice.RemoveString(updated.Finalizers, finalizer)
		if err := cli.Update(ctx, updated); err != nil {
			return fmt.Errorf("failed to remove finalizer %s from sesame %s/%s: %w",
				finalizer, sesame.Namespace, sesame.Name, err)
		}
	}
	return nil
}
