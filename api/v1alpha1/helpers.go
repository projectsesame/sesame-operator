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

package v1alpha1

const (
	// GatewayClassControllerRef identifies sesame operator as the managing controller
	// of a GatewayClass.
	// DEPRECATED: The sesame operator no longer reconciles GatewayClasses.
	GatewayClassControllerRef = "projectsesame.io/sesame-operator"

	// GatewayClassParamsRefGroup identifies sesame operator as the group name of a
	// GatewayClass.
	// DEPRECATED: The sesame operator no longer reconciles GatewayClasses.
	GatewayClassParamsRefGroup = "operator.projectsesame.io"

	// GatewayClassParamsRefKind identifies Sesame as the kind name of a GatewayClass.
	// DEPRECATED: The sesame operator no longer reconciles GatewayClasses.
	GatewayClassParamsRefKind = "Sesame"

	// GatewayFinalizer is the name of the finalizer used for a Gateway.
	// DEPRECATED: The sesame operator no longer reconciles Gateways.
	GatewayFinalizer = "gateway.networking.x-k8s.io/finalizer"

	// OwningGatewayNameLabel is the owner reference label used for a Gateway
	// managed by the operator. The value should be the name of the Gateway.
	// DEPRECATED: The sesame operator no longer reconciles Gateways.
	OwningGatewayNameLabel = "sesame.operator.projectsesame.io/owning-gateway-name"

	// OwningGatewayNsLabel is the owner reference label used for a Gateway
	// managed by the operator. The value should be the namespace of the Gateway.
	// DEPRECATED: The sesame operator no longer reconciles Gateways.
	OwningGatewayNsLabel = "sesame.operator.projectsesame.io/owning-gateway-namespace"
)

// IsFinalized returns true if Sesame is finalized.
func (c *Sesame) IsFinalized() bool {
	for _, f := range c.Finalizers {
		if f == SesameFinalizer {
			return true
		}
	}
	return false
}

// GatewayClassSet returns true if gatewayClassRef is set for Sesame.
// DEPRECATED: The GatewayClassRef field is deprecated.
func (c *Sesame) GatewayClassSet() bool {
	return c.Spec.GatewayClassRef != nil
}

// SesameNodeSelectorExists returns true if a nodeSelector is specified for Sesame.
func (c *Sesame) SesameNodeSelectorExists() bool {
	if c.Spec.NodePlacement != nil &&
		c.Spec.NodePlacement.Sesame != nil &&
		c.Spec.NodePlacement.Sesame.NodeSelector != nil {
		return true
	}

	return false
}

// SesameTolerationsExist returns true if tolerations are set for Sesame.
func (c *Sesame) SesameTolerationsExist() bool {
	if c.Spec.NodePlacement != nil &&
		c.Spec.NodePlacement.Sesame != nil &&
		len(c.Spec.NodePlacement.Sesame.Tolerations) > 0 {
		return true
	}

	return false
}

// EnvoyNodeSelectorExists returns true if a nodeSelector is specified for Envoy.
func (c *Sesame) EnvoyNodeSelectorExists() bool {
	if c.Spec.NodePlacement != nil &&
		c.Spec.NodePlacement.Envoy != nil &&
		c.Spec.NodePlacement.Envoy.NodeSelector != nil {
		return true
	}

	return false
}

// EnvoyTolerationsExist returns true if tolerations are set for Envoy.
func (c *Sesame) EnvoyTolerationsExist() bool {
	if c.Spec.NodePlacement != nil &&
		c.Spec.NodePlacement.Envoy != nil &&
		len(c.Spec.NodePlacement.Envoy.Tolerations) > 0 {
		return true
	}

	return false
}
