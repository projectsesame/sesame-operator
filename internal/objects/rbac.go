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

package objects

import (
	"context"
	"fmt"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	objcr "github.com/projectsesame/sesame-operator/internal/objects/clusterrole"
	objcrb "github.com/projectsesame/sesame-operator/internal/objects/clusterrolebinding"
	objrole "github.com/projectsesame/sesame-operator/internal/objects/role"
	objrb "github.com/projectsesame/sesame-operator/internal/objects/rolebinding"
	objsa "github.com/projectsesame/sesame-operator/internal/objects/serviceaccount"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	"github.com/projectsesame/sesame-operator/pkg/labels"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SesameRbacName is the name used for Sesame RBAC resources.
	SesameRbacName = "sesame"
	// EnvoyRbacName is the name used for Envoy RBAC resources.
	EnvoyRbacName = "envoy"
	// CertGenRbacName is the name used for Sesame certificate
	// generation RBAC resources.
	CertGenRbacName = "sesame-certgen"
)

// EnsureRBAC ensures all the necessary RBAC resources exist for the
// provided sesame.
func EnsureRBAC(ctx context.Context, cli client.Client, sesame *operatorv1alpha1.Sesame) error {
	ns := sesame.Spec.Namespace.Name
	names := []string{SesameRbacName, EnvoyRbacName, CertGenRbacName}
	certSvcAct := &corev1.ServiceAccount{}
	for _, name := range names {
		svcAct, err := objsa.EnsureServiceAccount(ctx, cli, name, sesame)
		if err != nil {
			return fmt.Errorf("failed to ensure service account %s/%s: %w", ns, name, err)
		}
		if svcAct.Name == CertGenRbacName {
			certSvcAct = svcAct
		}
	}
	// ClusterRole and ClusterRoleBinding resources are namespace-named to allow ownership
	// from individual instances of Sesame.
	nsName := fmt.Sprintf("%s-%s", SesameRbacName, sesame.Spec.Namespace.Name)
	cr, err := objcr.EnsureClusterRole(ctx, cli, nsName, sesame)
	if err != nil {
		return fmt.Errorf("failed to ensure cluster role %s: %w", SesameRbacName, err)
	}
	if err := objcrb.EnsureClusterRoleBinding(ctx, cli, nsName, cr.Name, SesameRbacName, sesame); err != nil {
		return fmt.Errorf("failed to ensure cluster role binding %s: %w", SesameRbacName, err)
	}
	certRole, err := objrole.EnsureRole(ctx, cli, CertGenRbacName, sesame)
	if err != nil {
		return fmt.Errorf("failed to ensure role %s/%s: %w", ns, CertGenRbacName, err)
	}
	if err := objrb.EnsureRoleBinding(ctx, cli, SesameRbacName, certSvcAct.Name, certRole.Name, sesame); err != nil {
		return fmt.Errorf("failed to ensure role binding %s/%s: %w", ns, SesameRbacName, err)
	}
	return nil
}

// EnsureRBACDeleted ensures all the necessary RBAC resources for the provided
// sesame are deleted if Sesame owner labels exist.
func EnsureRBACDeleted(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	var errs []error
	ns := Sesame.Spec.Namespace.Name
	objectsToDelete := []client.Object{}
	SesamesExist, err := objSesame.OtherSesamesExistInSpecNs(ctx, cli, Sesame)
	if err != nil {
		return fmt.Errorf("failed to verify if Sesames SesamesExist in namespace %s: %w",
			Sesame.Spec.Namespace.Name, err)
	}
	if !SesamesExist {
		cntrRoleBind, err := objrb.CurrentRoleBinding(ctx, cli, ns, SesameRbacName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}
		if cntrRoleBind != nil {
			objectsToDelete = append(objectsToDelete, cntrRoleBind)
		}
		cntrRole, err := objrole.CurrentRole(ctx, cli, ns, SesameRbacName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}
		if cntrRole != nil {
			objectsToDelete = append(objectsToDelete, cntrRole)
		}
		certRole, err := objrole.CurrentRole(ctx, cli, ns, CertGenRbacName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}
		if certRole != nil {
			objectsToDelete = append(objectsToDelete, certRole)
		}
		names := []string{SesameRbacName, EnvoyRbacName, CertGenRbacName}
		for _, name := range names {
			svcAct, err := objsa.CurrentServiceAccount(ctx, cli, ns, name)
			if err != nil {
				if !errors.IsNotFound(err) {
					return err
				}
			}
			if svcAct != nil {
				objectsToDelete = append(objectsToDelete, svcAct)
			}
		}
	}
	SesamesExist, _, err = objSesame.OtherSesamesExist(ctx, cli, Sesame)
	if err != nil {
		return fmt.Errorf("failed to verify if Sesames exist in any namespace: %w", err)
	}
	if !SesamesExist {
		// ClusterRole and ClusterRoleBinding resources are namespace-named to allow ownership
		// from individual instances of Sesame.
		nsName := fmt.Sprintf("%s-%s", SesameRbacName, Sesame.Spec.Namespace.Name)
		crb, err := objcrb.CurrentClusterRoleBinding(ctx, cli, nsName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}
		if crb != nil {
			objectsToDelete = append(objectsToDelete, crb)
		}
		cr, err := objcr.CurrentClusterRole(ctx, cli, nsName)
		if err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}
		if cr != nil {
			objectsToDelete = append(objectsToDelete, cr)
		}
	}
	for _, object := range objectsToDelete {
		kind := object.GetObjectKind().GroupVersionKind().Kind
		namespace := object.(metav1.Object).GetNamespace()
		name := object.(metav1.Object).GetName()
		if labels.Exist(object, objSesame.OwnerLabels(Sesame)) {
			if err := cli.Delete(ctx, object); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to delete %s %s/%s: %w", kind, namespace, name, err)
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}
