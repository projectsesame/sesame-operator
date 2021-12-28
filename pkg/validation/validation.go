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

package validation

import (
	"context"
	"fmt"
	"net"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	"github.com/projectsesame/sesame-operator/pkg/slice"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Sesame returns true if sesame is valid.
func Sesame(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	// TODO [danehans]: Remove when https://github.com/projectsesame/sesame-operator/issues/18 is fixed.
	exist, err := objSesame.OtherSesamesExistInSpecNs(ctx, cli, Sesame)
	if err != nil {
		return fmt.Errorf("failed to verify if other Sesames exist in namespace %s: %w",
			Sesame.Spec.Namespace.Name, err)
	}
	if exist {
		return fmt.Errorf("other Sesames exist in namespace %s", Sesame.Spec.Namespace.Name)
	}

	if err := ContainerPorts(Sesame); err != nil {
		return err
	}

	if Sesame.Spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.NodePortServicePublishingType {
		if err := NodePorts(Sesame); err != nil {
			return err
		}
	}

	if Sesame.Spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.LoadBalancerServicePublishingType {
		if err := LoadBalancerAddress(Sesame); err != nil {
			return err
		}
		if err := LoadBalancerProvider(Sesame); err != nil {
			return err
		}
	}

	return nil
}

// ContainerPorts validates container ports of sesame, returning an
// error if the container ports do not meet the API specification.
func ContainerPorts(Sesame *operatorv1alpha1.Sesame) error {
	var numsFound []int32
	var namesFound []string
	httpFound := false
	httpsFound := false
	for _, port := range Sesame.Spec.NetworkPublishing.Envoy.ContainerPorts {
		if len(numsFound) > 0 && slice.ContainsInt32(numsFound, port.PortNumber) {
			return fmt.Errorf("duplicate container port number %d", port.PortNumber)
		}
		numsFound = append(numsFound, port.PortNumber)
		if len(namesFound) > 0 && slice.ContainsString(namesFound, port.Name) {
			return fmt.Errorf("duplicate container port name %q", port.Name)
		}
		namesFound = append(namesFound, port.Name)
		switch {
		case port.Name == "http":
			httpFound = true
		case port.Name == "https":
			httpsFound = true
		}
	}
	if httpFound && httpsFound {
		return nil
	}
	return fmt.Errorf("http and https container ports are unspecified")
}

// NodePorts validates nodeports of sesame, returning an error if the nodeports
// do not meet the API specification.
func NodePorts(Sesame *operatorv1alpha1.Sesame) error {
	ports := Sesame.Spec.NetworkPublishing.Envoy.NodePorts
	if ports == nil {
		// When unspecified, API server will auto-assign port numbers.
		return nil
	}
	for _, p := range ports {
		if p.Name != "http" && p.Name != "https" {
			return fmt.Errorf("invalid port name %q; only \"http\" and \"https\" are supported", p.Name)
		}
	}
	if ports[0].Name == ports[1].Name {
		return fmt.Errorf("duplicate nodeport names detected")
	}
	if ports[0].PortNumber != nil && ports[1].PortNumber != nil {
		if ports[0].PortNumber == ports[1].PortNumber {
			return fmt.Errorf("duplicate nodeport port numbers detected")
		}
	}

	return nil
}

// LoadBalancerAddress validates LoadBalancer "address" parameter of sesame, returning an
// error if "address" does not meet the API specification.
func LoadBalancerAddress(Sesame *operatorv1alpha1.Sesame) error {
	if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AzureLoadBalancerProvider &&
		Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure != nil &&
		Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.Address != nil {
		validationIP := net.ParseIP(*Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.Address)
		if validationIP == nil {
			return fmt.Errorf("wrong LoadBalancer address format, should be string with IPv4 or IPv6 format")
		}
	} else if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.GCPLoadBalancerProvider &&
		Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP != nil &&
		Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP.Address != nil {
		validationIP := net.ParseIP(*Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP.Address)
		if validationIP == nil {
			return fmt.Errorf("wrong LoadBalancer address format, should be string with IPv4 or IPv6 format")
		}
	}

	return nil
}

// LoadBalancerProvider validates LoadBalancer provider parameters of sesame, returning
// and error if parameters for different provider are specified the for the one specified
// with "type" parameter.
func LoadBalancerProvider(Sesame *operatorv1alpha1.Sesame) error {
	switch Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type {
	case operatorv1alpha1.AWSLoadBalancerProvider:
		if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure != nil ||
			Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP != nil {
			return fmt.Errorf("aws provider chosen, other providers parameters should not be specified")
		}
	case operatorv1alpha1.AzureLoadBalancerProvider:
		if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.AWS != nil ||
			Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP != nil {
			return fmt.Errorf("azure provider chosen, other providers parameters should not be specified")
		}
	case operatorv1alpha1.GCPLoadBalancerProvider:
		if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.AWS != nil ||
			Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure != nil {
			return fmt.Errorf("gcp provider chosen, other providers parameters should not be specified")
		}
	}

	return nil
}
