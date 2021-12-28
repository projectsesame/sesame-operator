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

package service

import (
	"context"
	"fmt"
	"strings"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/internal/equality"
	objds "github.com/projectsesame/sesame-operator/internal/objects/daemonset"
	objdeploy "github.com/projectsesame/sesame-operator/internal/objects/deployment"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	objcfg "github.com/projectsesame/sesame-operator/internal/objects/sharedconfig"
	"github.com/projectsesame/sesame-operator/pkg/labels"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SesameSvcName is the name of Sesame's Service.
	// [TODO] danehans: Update Sesame name to sesame.Name + "-sesame" to support multiple
	// Sesames/ns when https://github.com/projectsesame/Sesame/issues/2122 is fixed.
	SesameSvcName = "sesame"
	// [TODO] danehans: Update Envoy name to sesame.Name + "-envoy" to support multiple
	// Sesames/ns when https://github.com/projectsesame/Sesame/issues/2122 is fixed.
	// envoySvcName is the name of Envoy's Service.
	envoySvcName = "envoy"
	// awsLbBackendProtoAnnotation is a Service annotation that places the AWS ELB into
	// "TCP" mode so that it does not do HTTP negotiation for HTTPS connections at the
	// ELB edge. The downside of this is the remote IP address of all connections will
	// appear to be the internal address of the ELB.
	// TODO [danehans]: Make proxy protocol configurable or automatically enabled. See
	// https://github.com/projectsesame/sesame-operator/issues/49 for details.
	awsLbBackendProtoAnnotation = "service.beta.kubernetes.io/aws-load-balancer-backend-protocol"
	// awsLBTypeAnnotation is a Service annotation used to specify an AWS load
	// balancer type. See the following for additional details:
	// https://kubernetes.io/docs/concepts/services-networking/service/#aws-nlb-support
	awsLBTypeAnnotation = "service.beta.kubernetes.io/aws-load-balancer-type"
	// awsLBProxyProtocolAnnotation is used to enable the PROXY protocol for an AWS Classic
	// load balancer. For additional details, see:
	// https://kubernetes.io/docs/concepts/services-networking/service/#proxy-protocol-support-on-aws
	awsLBProxyProtocolAnnotation = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"
	// awsLBAllocationIDsAnnotation is a Service annotation that provides capability to
	// assign Load Balancer IP based on Allocation IDs of AWS Elastic IP resources when
	// load balancer scope is set to "External"
	awsLBAllocationIDsAnnotation = "service.beta.kubernetes.io/aws-load-balancer-eip-allocations"
	// awsInternalLBAnnotation is the annotation used on a service to specify an AWS
	// load balancer as being internal.
	awsInternalLBAnnotation = "service.beta.kubernetes.io/aws-load-balancer-internal"
	// azureLBResourceGroupAnnotation is a Service annotation that provides capability
	// to assign Load Balancer IP based on Public IP Azure resource that resides in
	// different resource group as AKS cluster when load balancer scope is set to "External".
	azureLBResourceGroupAnnotation = "service.beta.kubernetes.io/azure-load-balancer-resource-group"
	// azureLBSubnetAnnotation is a Service annotation that provides capability to assign
	// Load Balancer IP based on desired subnet when load balancer scope is set to "Internal".
	azureLBSubnetAnnotation = "service.beta.kubernetes.io/azure-load-balancer-internal-subnet"
	// azureInternalLBAnnotation is the annotation used on a service to specify an Azure
	// load balancer as being internal.
	azureInternalLBAnnotation = "service.beta.kubernetes.io/azure-load-balancer-internal"
	// gcpLBSubnetAnnotation is a Service annotation that provides capability to assign
	// Load Balancer IP to specified subnet when load balancer scope is set to "Internal".
	gcpLBSubnetAnnotation = "networking.gke.io/internal-load-balancer-subnet"
	// gcpLBTypeAnnotationLegacy is the annotation used on a service to specify a GCP load balancer
	// type for GKE version earlier then 1.17.
	gcpLBTypeAnnotationLegacy = "cloud.google.com/load-balancer-type"
	// gcpLBTypeAnnotation is the annotation used on a service to specify a GCP load balancer
	// type for GKE version 1.17 and later.
	gcpLBTypeAnnotation = "networking.gke.io/load-balancer-type"
	// EnvoyServiceHTTPPort is the HTTP port number of the Envoy service.
	EnvoyServiceHTTPPort = int32(80)
	// EnvoyServiceHTTPSPort is the HTTPS port number of the Envoy service.
	EnvoyServiceHTTPSPort = int32(443)
	// EnvoyNodePortHTTPPort is the NodePort port number for Envoy's HTTP service. For NodePort
	// details see: https://kubernetes.io/docs/concepts/services-networking/service/#nodeport
	EnvoyNodePortHTTPPort = int32(30080)
	// EnvoyNodePortHTTPSPort is the NodePort port number for Envoy's HTTPS service. For NodePort
	// details see: https://kubernetes.io/docs/concepts/services-networking/service/#nodeport
	EnvoyNodePortHTTPSPort = int32(30443)
)

var (
	// InternalLBAnnotations maps cloud providers to the provider's annotation
	// key/value pair used for managing an internal load balancer. For additional
	// details see:
	//  https://kubernetes.io/docs/concepts/services-networking/service/#internal-load-balancer
	//
	InternalLBAnnotations = map[operatorv1alpha1.LoadBalancerProviderType]map[string]string{
		operatorv1alpha1.AWSLoadBalancerProvider: {
			awsInternalLBAnnotation: "true",
		},
		operatorv1alpha1.AzureLoadBalancerProvider: {
			// Azure load balancers are not customizable and are set to (2 fail @ 5s interval, 2 healthy)
			azureInternalLBAnnotation: "true",
		},
		operatorv1alpha1.GCPLoadBalancerProvider: {
			gcpLBTypeAnnotation:       "Internal",
			gcpLBTypeAnnotationLegacy: "Internal",
		},
	}
)

// EnsureSesameService ensures that a Sesame Service exists for the given sesame.
func EnsureSesameService(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	desired := DesiredSesameService(Sesame)
	current, err := currentSesameService(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return createService(ctx, cli, desired)
		}
		return fmt.Errorf("failed to get service %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if err := updateSesameServiceIfNeeded(ctx, cli, Sesame, current, desired); err != nil {
		return fmt.Errorf("failed to update service %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	return nil
}

// EnsureEnvoyService ensures that an Envoy Service exists for the given sesame.
func EnsureEnvoyService(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	desired := DesiredEnvoyService(Sesame)
	current, err := currentEnvoyService(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return createService(ctx, cli, desired)
		}
		return fmt.Errorf("failed to get service %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if err := updateEnvoyServiceIfNeeded(ctx, cli, Sesame, current, desired); err != nil {
		return fmt.Errorf("failed to update service %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	return nil
}

// EnsureSesameServiceDeleted ensures that a Sesame Service for the
// provided sesame is deleted if Sesame owner labels exist.
func EnsureSesameServiceDeleted(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	svc, err := currentSesameService(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if labels.Exist(svc, objSesame.OwnerLabels(Sesame)) {
		if err := cli.Delete(ctx, svc); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// EnsureEnvoyServiceDeleted ensures that an Envoy Service for the
// provided sesame is deleted.
func EnsureEnvoyServiceDeleted(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	svc, err := currentEnvoyService(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if labels.Exist(svc, objSesame.OwnerLabels(Sesame)) {
		if err := cli.Delete(ctx, svc); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// DesiredSesameService generates the desired Sesame Service for the given sesame.
func DesiredSesameService(Sesame *operatorv1alpha1.Sesame) *corev1.Service {
	xdsPort := objcfg.XDSPort
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: Sesame.Spec.Namespace.Name,
			Name:      SesameSvcName,
			Labels: map[string]string{
				operatorv1alpha1.OwningSesameNameLabel: Sesame.Name,
				operatorv1alpha1.OwningSesameNsLabel:   Sesame.Namespace,
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "xds",
					Port:       xdsPort,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.IntOrString{IntVal: xdsPort},
				},
			},
			Selector:        objdeploy.SesameDeploymentPodSelector().MatchLabels,
			Type:            corev1.ServiceTypeClusterIP,
			SessionAffinity: corev1.ServiceAffinityNone,
		},
	}
	return svc
}

// DesiredEnvoyService generates the desired Envoy Service for the given sesame.
func DesiredEnvoyService(Sesame *operatorv1alpha1.Sesame) *corev1.Service {
	var ports []corev1.ServicePort
	for _, port := range Sesame.Spec.NetworkPublishing.Envoy.ContainerPorts {
		var p corev1.ServicePort
		httpFound := false
		httpsFound := false
		switch {
		case httpsFound && httpFound:
			break
		case port.Name == "http":
			httpFound = true
			p.Name = port.Name
			p.Port = EnvoyServiceHTTPPort
			p.Protocol = corev1.ProtocolTCP
			p.TargetPort = intstr.IntOrString{IntVal: port.PortNumber}
			ports = append(ports, p)
		case port.Name == "https":
			httpsFound = true
			p.Name = port.Name
			p.Port = EnvoyServiceHTTPSPort
			p.Protocol = corev1.ProtocolTCP
			p.TargetPort = intstr.IntOrString{IntVal: port.PortNumber}
			ports = append(ports, p)
		}
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   Sesame.Spec.Namespace.Name,
			Name:        envoySvcName,
			Annotations: map[string]string{},
			Labels: map[string]string{
				operatorv1alpha1.OwningSesameNameLabel: Sesame.Name,
				operatorv1alpha1.OwningSesameNsLabel:   Sesame.Namespace,
			},
		},
		Spec: corev1.ServiceSpec{
			Ports:           ports,
			Selector:        objds.EnvoyDaemonSetPodSelector().MatchLabels,
			SessionAffinity: corev1.ServiceAffinityNone,
		},
	}

	// Add AWS LB annotations based on the network publishing strategy and provider type.
	if Sesame.Spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.LoadBalancerServicePublishingType &&
		Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AWSLoadBalancerProvider {
		// Add the TCP backend protocol annotation for AWS classic load balancers.
		if isELB(&Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters) {
			svc.Annotations[awsLbBackendProtoAnnotation] = "tcp"
			svc.Annotations[awsLBProxyProtocolAnnotation] = "*"
		} else {
			// Annotate the service for an NLB.
			svc.Annotations[awsLBTypeAnnotation] = "nlb"
		}
	}

	// Add the AllocationIDs annotation if specified by AWS provider parameters.
	if allocationIDsNeeded(&Sesame.Spec) {
		svc.Annotations[awsLBAllocationIDsAnnotation] = strings.Join(Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.AWS.AllocationIDs, ",")
	}

	// Add the ResourceGroup annotation if specified by Azure provider parameters.
	if resourceGroupNeeded(&Sesame.Spec) {
		svc.Annotations[azureLBResourceGroupAnnotation] = *Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.ResourceGroup
	}

	// Add the Subnet annotation if specified by provider parameters.
	if subnetNeeded(&Sesame.Spec) {
		if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AzureLoadBalancerProvider {
			svc.Annotations[azureLBSubnetAnnotation] = *Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.Subnet
		} else if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.GCPLoadBalancerProvider {
			svc.Annotations[gcpLBSubnetAnnotation] = *Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP.Subnet
		}
	}

	// Add LoadBalancerIP parameter if specified by provider parameters.
	if loadBalancerAddressNeeded(&Sesame.Spec) {
		if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AzureLoadBalancerProvider {
			svc.Spec.LoadBalancerIP = *Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.Address
		} else if Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.GCPLoadBalancerProvider {
			svc.Spec.LoadBalancerIP = *Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP.Address
		}
	}

	epType := Sesame.Spec.NetworkPublishing.Envoy.Type
	if epType == operatorv1alpha1.LoadBalancerServicePublishingType ||
		epType == operatorv1alpha1.NodePortServicePublishingType {
		svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeLocal
	}
	switch epType {
	case operatorv1alpha1.LoadBalancerServicePublishingType:
		svc.Spec.Type = corev1.ServiceTypeLoadBalancer
		isInternal := Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.Scope == operatorv1alpha1.InternalLoadBalancer
		if isInternal {
			provider := Sesame.Spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type
			internalAnnotations := InternalLBAnnotations[provider]
			for name, value := range internalAnnotations {
				svc.Annotations[name] = value
			}
		}
	case operatorv1alpha1.NodePortServicePublishingType:
		svc.Spec.Type = corev1.ServiceTypeNodePort
		if len(Sesame.Spec.NetworkPublishing.Envoy.NodePorts) > 0 {
			for _, p := range Sesame.Spec.NetworkPublishing.Envoy.NodePorts {
				if p.PortNumber != nil {
					for i, q := range svc.Spec.Ports {
						if q.Name == p.Name {
							svc.Spec.Ports[i].NodePort = *p.PortNumber
						}
					}
				}
			}
		}
	case operatorv1alpha1.ClusterIPServicePublishingType:
		svc.Spec.Type = corev1.ServiceTypeClusterIP
	}
	return svc
}

// currentSesameService returns the current Sesame Service for the provided sesame.
func currentSesameService(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) (*corev1.Service, error) {
	current := &corev1.Service{}
	key := types.NamespacedName{
		Namespace: Sesame.Spec.Namespace.Name,
		Name:      SesameSvcName,
	}
	err := cli.Get(ctx, key, current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

// currentEnvoyService returns the current Envoy Service for the provided sesame.
func currentEnvoyService(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) (*corev1.Service, error) {
	current := &corev1.Service{}
	key := types.NamespacedName{
		Namespace: Sesame.Spec.Namespace.Name,
		Name:      envoySvcName,
	}
	err := cli.Get(ctx, key, current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

// createService creates a Service resource for the provided svc.
func createService(ctx context.Context, cli client.Client, svc *corev1.Service) error {
	if err := cli.Create(ctx, svc); err != nil {
		return fmt.Errorf("failed to create service %s/%s: %w", svc.Namespace, svc.Name, err)
	}
	return nil
}

// updateSesameServiceIfNeeded updates a Sesame Service if current does not match desired.
func updateSesameServiceIfNeeded(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, current, desired *corev1.Service) error {
	if labels.Exist(current, objSesame.OwnerLabels(Sesame)) {
		_, updated := equality.ClusterIPServiceChanged(current, desired)
		if updated {
			if err := cli.Update(ctx, desired); err != nil {
				return fmt.Errorf("failed to update service %s/%s: %w", desired.Namespace, desired.Name, err)
			}
			return nil
		}
	}
	return nil
}

// updateEnvoyServiceIfNeeded updates an Envoy Service if current does not match desired,
// using sesame to verify the existence of owner labels.
func updateEnvoyServiceIfNeeded(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, current, desired *corev1.Service) error {
	if labels.Exist(current, objSesame.OwnerLabels(Sesame)) {
		// Using the Service returned by the equality pkg instead of the desired
		// parameter since clusterIP is immutable.
		var updated *corev1.Service
		needed := false
		switch Sesame.Spec.NetworkPublishing.Envoy.Type {
		case operatorv1alpha1.NodePortServicePublishingType:
			updated, needed = equality.NodePortServiceChanged(current, desired)
		case operatorv1alpha1.ClusterIPServicePublishingType:
			updated, needed = equality.ClusterIPServiceChanged(current, desired)
		// Add additional network publishing types as they are introduced.
		default:
			// LoadBalancerService is the default network publishing type.
			updated, needed = equality.LoadBalancerServiceChanged(current, desired)
		}
		if needed {
			if err := cli.Update(ctx, updated); err != nil {
				return fmt.Errorf("failed to update service %s/%s: %w", desired.Namespace, desired.Name, err)
			}
			return nil
		}
	}
	return nil
}

// isELB returns true if params is an AWS Classic ELB.
func isELB(params *operatorv1alpha1.ProviderLoadBalancerParameters) bool {
	return params.Type == operatorv1alpha1.AWSLoadBalancerProvider &&
		(params.AWS == nil || params.AWS.Type == operatorv1alpha1.AWSClassicLoadBalancer)
}

// allocationIDsNeeded returns true if "service.beta.kubernetes.io/aws-load-balancer-eip-allocations"
// annotation is needed based on the provided spec.
func allocationIDsNeeded(spec *operatorv1alpha1.SesameSpec) bool {
	return spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.LoadBalancerServicePublishingType &&
		spec.NetworkPublishing.Envoy.LoadBalancer.Scope == "External" &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AWSLoadBalancerProvider &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.AWS != nil &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.AWS.Type == operatorv1alpha1.AWSNetworkLoadBalancer &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.AWS.AllocationIDs != nil
}

// resourceGroupNeeded returns true if "service.beta.kubernetes.io/azure-load-balancer-resource-group"
// annotation is needed based on the provided spec.
func resourceGroupNeeded(spec *operatorv1alpha1.SesameSpec) bool {
	return spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.LoadBalancerServicePublishingType &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AzureLoadBalancerProvider &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure != nil &&
		spec.NetworkPublishing.Envoy.LoadBalancer.Scope == "External" &&
		spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.ResourceGroup != nil
}

// subnetNeeded returns true if "service.beta.kubernetes.io/azure-load-balancer-internal-subnet" or
// "networking.gke.io/internal-load-balancer-subnet" annotation is needed based
// on the provided spec.
func subnetNeeded(spec *operatorv1alpha1.SesameSpec) bool {
	return spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.LoadBalancerServicePublishingType &&
		spec.NetworkPublishing.Envoy.LoadBalancer.Scope == "Internal" &&
		((spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AzureLoadBalancerProvider &&
			spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure != nil &&
			spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.Subnet != nil) ||
			(spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.GCPLoadBalancerProvider &&
				spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP != nil &&
				spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP.Subnet != nil))
}

// loadBalancerAddressNeeded returns true if LoadBalancerIP parameter of service
// is needed based on provided spec.
func loadBalancerAddressNeeded(spec *operatorv1alpha1.SesameSpec) bool {
	return spec.NetworkPublishing.Envoy.Type == operatorv1alpha1.LoadBalancerServicePublishingType &&
		((spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.AzureLoadBalancerProvider &&
			spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure != nil &&
			spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Azure.Address != nil) ||
			(spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.Type == operatorv1alpha1.GCPLoadBalancerProvider &&
				spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP != nil &&
				spec.NetworkPublishing.Envoy.LoadBalancer.ProviderParameters.GCP.Address != nil))
}
