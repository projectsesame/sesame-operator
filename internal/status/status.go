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

package status

import (
	"context"
	"fmt"
	"strings"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/internal/equality"
	objds "github.com/projectsesame/sesame-operator/internal/objects/daemonset"
	objdeploy "github.com/projectsesame/sesame-operator/internal/objects/deployment"
	retryable "github.com/projectsesame/sesame-operator/internal/retryableerror"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syncSesameStatus computes the current status of sesame and updates status upon
// any changes since last sync.
func SyncSesame(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	var err error
	var errs []error

	latest := &operatorv1alpha1.Sesame{}
	key := types.NamespacedName{
		Namespace: Sesame.Namespace,
		Name:      Sesame.Name,
	}
	if err := cli.Get(ctx, key, latest); err != nil {
		if errors.IsNotFound(err) {
			// The sesame may have been deleted during status sync.
			return nil
		}
		return fmt.Errorf("failed to get sesame %s/%s: %w", Sesame.Namespace, Sesame.Name, err)
	}

	updated := latest.DeepCopy()

	deploy, err := objdeploy.CurrentDeployment(ctx, cli, latest)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to get deployment for sesame %s/%s status: %w", latest.Namespace, latest.Name, err))
	} else {
		updated.Status.AvailableSesames = deploy.Status.AvailableReplicas
	}
	ds, err := objds.CurrentDaemonSet(ctx, cli, latest)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to get daemonset for sesame %s/%s status: %w", latest.Namespace, latest.Name, err))
	} else {
		updated.Status.AvailableEnvoys = ds.Status.NumberAvailable
	}

	updated.Status.Conditions = mergeConditions(updated.Status.Conditions,
		computeSesameAvailableCondition(deploy, ds))

	if equality.SesameStatusChanged(latest.Status, updated.Status) {
		if err := cli.Status().Update(ctx, updated); err != nil {
			switch {
			case errors.IsNotFound(err):
				// The sesame may have been deleted during status sync.
				return retryable.NewMaybeRetryableAggregate(errs)
			case strings.Contains(err.Error(), "the object has been modified"):
				// Retry if the object was modified during status sync.
				if err := SyncSesame(ctx, cli, updated); err != nil {
					errs = append(errs, fmt.Errorf("failed to update sesame %s/%s status: %w", latest.Namespace,
						latest.Name, err))
				}
			default:
				errs = append(errs, fmt.Errorf("failed to update sesame %s/%s status: %w", latest.Namespace,
					latest.Name, err))
			}
		}
	}

	return retryable.NewMaybeRetryableAggregate(errs)
}
