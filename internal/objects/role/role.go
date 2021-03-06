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

package role

import (
	"context"
	"fmt"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	equality "github.com/projectsesame/sesame-operator/internal/equality"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	"github.com/projectsesame/sesame-operator/pkg/labels"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureRole ensures a Role resource exists with the provided name/ns
// and sesame namespace/name for the owning sesame labels.
func EnsureRole(ctx context.Context, cli client.Client, name string, Sesame *operatorv1alpha1.Sesame) (*rbacv1.Role, error) {
	desired := desiredRole(name, Sesame)
	current, err := CurrentRole(ctx, cli, Sesame.Spec.Namespace.Name, name)
	if err != nil {
		if errors.IsNotFound(err) {
			updated, err := createRole(ctx, cli, desired)
			if err != nil {
				return nil, fmt.Errorf("failed to create role %s/%s: %w", desired.Namespace, desired.Name, err)
			}
			return updated, nil
		}
		return nil, fmt.Errorf("failed to get role %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	updated, err := updateRoleIfNeeded(ctx, cli, Sesame, current, desired)
	if err != nil {
		return nil, fmt.Errorf("failed to update role %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	return updated, nil
}

// desiredRole constructs an instance of the desired ClusterRole resource with the
// provided ns/name and sesame namespace/name for the owning sesame labels.
func desiredRole(name string, Sesame *operatorv1alpha1.Sesame) *rbacv1.Role {
	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			Kind: "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: Sesame.Spec.Namespace.Name,
			Name:      name,
		},
	}
	groupAll := []string{""}
	verbCU := []string{"create", "update"}
	secret := rbacv1.PolicyRule{
		Verbs:     verbCU,
		APIGroups: groupAll,
		Resources: []string{"secrets"},
	}
	role.Rules = []rbacv1.PolicyRule{secret}
	role.Labels = map[string]string{
		operatorv1alpha1.OwningSesameNameLabel: Sesame.Name,
		operatorv1alpha1.OwningSesameNsLabel:   Sesame.Namespace,
	}
	return role
}

// CurrentRole returns the current Role for the provided ns/name.
func CurrentRole(ctx context.Context, cli client.Client, ns, name string) (*rbacv1.Role, error) {
	current := &rbacv1.Role{}
	key := types.NamespacedName{
		Namespace: ns,
		Name:      name,
	}
	err := cli.Get(ctx, key, current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

// createRole creates a Role resource for the provided role.
func createRole(ctx context.Context, cli client.Client, role *rbacv1.Role) (*rbacv1.Role, error) {
	if err := cli.Create(ctx, role); err != nil {
		return nil, fmt.Errorf("failed to create role %s/%s: %w", role.Namespace, role.Name, err)
	}
	return role, nil
}

// updateRoleIfNeeded updates a Role resource if current does not match desired,
// using sesame to verify the existence of owner labels.
func updateRoleIfNeeded(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, current, desired *rbacv1.Role) (*rbacv1.Role, error) {
	if labels.Exist(current, objSesame.OwnerLabels(Sesame)) {
		role, updated := equality.RoleConfigChanged(current, desired)
		if updated {
			if err := cli.Update(ctx, role); err != nil {
				return nil, fmt.Errorf("failed to update cluster role %s/%s: %w", role.Namespace, role.Name, err)
			}
			return role, nil
		}
	}
	return current, nil
}
