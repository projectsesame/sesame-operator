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

package clusterrole

import (
	"fmt"
	"testing"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"

	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

func checkClusterRoleName(t *testing.T, cr *rbacv1.ClusterRole, expected string) {
	t.Helper()

	if cr.Name == expected {
		return
	}

	t.Errorf("cluster role has unexpected name %q", cr.Name)
}

func checkClusterRoleLabels(t *testing.T, cr *rbacv1.ClusterRole, expected map[string]string) {
	t.Helper()

	if apiequality.Semantic.DeepEqual(cr.Labels, expected) {
		return
	}

	t.Errorf("cluster role has unexpected %q labels", cr.Labels)
}

func TestDesiredClusterRole(t *testing.T) {
	name := "test-cr"
	cfg := objSesame.Config{
		Name:        name,
		Namespace:   fmt.Sprintf("%s-ns", name),
		SpecNs:      "projectsesame",
		RemoveNs:    true,
		NetworkType: operatorv1alpha1.LoadBalancerServicePublishingType,
	}
	cntr := objSesame.New(cfg)
	cr := desiredClusterRole(name, cntr)
	checkClusterRoleName(t, cr, name)
	ownerLabels := map[string]string{
		operatorv1alpha1.OwningSesameNameLabel: cntr.Name,
		operatorv1alpha1.OwningSesameNsLabel:   cntr.Namespace,
	}
	checkClusterRoleLabels(t, cr, ownerLabels)
}
