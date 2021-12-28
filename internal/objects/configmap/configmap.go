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

package configmap

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	objsesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	"github.com/projectsesame/sesame-operator/pkg/labels"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SesameConfigMapName is the name of Sesame's ConfigMap resource.
	// [TODO] danehans: Remove and use sesame.Name when
	// https://github.com/projectsesame/Sesame/issues/2122 is fixed.
	SesameConfigMapName = "sesame"
)

var sesameConfigMapTemplate = template.Must(template.New("sesame.yaml").Parse(`#
# server:
#   determine which XDS Server implementation to utilize in Sesame.
#   xds-server-type: sesame
#
# Specify the Gateway API configuration.{{if .GatewayControllerName }}
gateway:
  controllerName: {{.GatewayControllerName}}{{else}}
# gateway:
#   controllerName: projectsesame.io/projectsesame/sesame{{end}}
#
# should sesame expect to be running inside a k8s cluster
# incluster: true
#
# path to kubeconfig (if not running inside a k8s cluster)
# kubeconfig: /path/to/.kube/config
#
# Disable RFC-compliant behavior to strip "Content-Length" header if
# "Tranfer-Encoding: chunked" is also set.
# disableAllowChunkedLength: false
# Disable HTTPProxy permitInsecure field
disablePermitInsecure: false
tls:
# minimum TLS version that Sesame will negotiate
# minimum-protocol-version: "1.2"
# TLS ciphers to be supported by Envoy TLS listeners when negotiating
# TLS 1.2.
# cipher-suites:
# - '[ECDHE-ECDSA-AES128-GCM-SHA256|ECDHE-ECDSA-CHACHA20-POLY1305]'
# - '[ECDHE-RSA-AES128-GCM-SHA256|ECDHE-RSA-CHACHA20-POLY1305]'
# - 'ECDHE-ECDSA-AES256-GCM-SHA384'
# - 'ECDHE-RSA-AES256-GCM-SHA384'
# Defines the Kubernetes name/namespace matching a secret to use
# as the fallback certificate when requests which don't match the
# SNI defined for a vhost.
  fallback-certificate:
#   name: fallback-secret-name
#   namespace: projectsesame
  envoy-client-certificate:
#   name: envoy-client-cert-secret-name
#   namespace: projectsesame
# The following config shows the defaults for the leader election.
# leaderelection:
#   configmap-name: leader-elect
#   configmap-namespace: projectsesame
####
# ExternalName Services are disabled by default due to CVE-2021-XXXXX
# You can re-enable them by setting this setting to "true".
# This is not recommended without understanding the security implications.
# Please see the advisory at https://github.com/projectsesame/sesame/security/advisories/GHSA-5ph6-qq5x-7jwc for the details.{{if .EnableExternalNameService }}
enableExternalNameService: {{.EnableExternalNameService}}{{else}}
# enableExternalNameService: false{{end}}
##
### Logging options
# Default setting
accesslog-format: envoy
# To enable JSON logging in Envoy
# accesslog-format: json
# The default fields that will be logged are specified below.
# To customize this list, just add or remove entries.
# The canonical list is available at
# https://godoc.org/github.com/projectsesame/sesame/internal/envoy#JSONFields
# json-fields:
#   - "@timestamp"
#   - "authority"
#   - "bytes_received"
#   - "bytes_sent"
#   - "downstream_local_address"
#   - "downstream_remote_address"
#   - "duration"
#   - "method"
#   - "path"
#   - "protocol"
#   - "request_id"
#   - "requested_server_name"
#   - "response_code"
#   - "response_flags"
#   - "uber_trace_id"
#   - "upstream_cluster"
#   - "upstream_host"
#   - "upstream_local_address"
#   - "upstream_service_time"
#   - "user_agent"
#   - "x_forwarded_for"
#
# default-http-versions:
# - "HTTP/2"
# - "HTTP/1.1"
#
# The following shows the default proxy timeout settings.
# timeouts:
#   request-timeout: infinity
#   connection-idle-timeout: 60s
#   stream-idle-timeout: 5m
#   max-connection-duration: infinity
#   delayed-close-timeout: 1s
#   connection-shutdown-grace-period: 5s
#
# Envoy cluster settings.
# cluster:
#   configure the cluster dns lookup family
#   valid options are: auto (default), v4, v6
#   dns-lookup-family: auto
#
# Envoy network settings.
# network:
#   Configure the number of additional ingress proxy hops from the
#   right side of the x-forwarded-for HTTP header to trust.
#   num-trusted-hops: 0
`))

// configMapParams contains everything needed to manage a Sesame ConfigMap.
type configMapParams struct {
	// Namespace is the namespace of the ConfigMap.
	Namespace string
	// Name is the name of the ConfigMap. Defaults to "sesame".
	Name string
	// Labels are labels to apply to the ConfigMap.
	Labels map[string]string
	// Sesame contains Sesame configuration parameters.
	Sesame sesameConfig
}

// sesameConfig contains Sesame configuration parameters.
type sesameConfig struct {
	// GatewayControllerName is the name of the controller that should
	// reconcile GatewayClasses and associated Gateways.
	GatewayControllerName string

	// EnableExternalNameService sets whether ExternalName Services are
	// allowed.
	EnableExternalNameService bool
}

// configForSesame returns a configMapParams with default fields set for sesame.
func configForSesame(Sesame *operatorv1alpha1.Sesame) *configMapParams {
	cfg := &configMapParams{Name: SesameConfigMapName}
	cfg.Namespace = Sesame.Spec.Namespace.Name
	labels := objsesame.OwnerLabels(Sesame)
	cfg.Labels = labels
	if Sesame.Spec.GatewayControllerName != nil {
		cfg.Sesame = sesameConfig{
			GatewayControllerName: *Sesame.Spec.GatewayControllerName,
		}
	}
	if Sesame.Spec.EnableExternalNameService != nil {
		cfg.Sesame.EnableExternalNameService = *Sesame.Spec.EnableExternalNameService
	}
	return cfg
}

// EnsureConfigMap ensures that a ConfigMap exists for the given sesame.
func EnsureConfigMap(ctx context.Context, cli client.Client, sesame *operatorv1alpha1.Sesame) error {
	cfg := configForSesame(sesame)
	desired, err := desired(cfg)
	if err != nil {
		return fmt.Errorf("failed to build configmap: %w", err)
	}
	current, err := current(ctx, cli, cfg)
	if err != nil {
		if errors.IsNotFound(err) {
			return create(ctx, cli, desired)
		}
		return fmt.Errorf("failed to get configmap %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if err := updateIfNeeded(ctx, cli, cfg, current, desired); err != nil {
		return fmt.Errorf("failed to update configmap %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	return nil
}

// EnsureConfigMapDeleted deletes a ConfigMap for the provided sesame, if the configured owner labels exist.
func EnsureConfigMapDeleted(ctx context.Context, cli client.Client, sesame *operatorv1alpha1.Sesame) error {
	cfg := configForSesame(sesame)
	cfgMap, err := current(ctx, cli, cfg)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if labels.Exist(cfgMap, cfg.Labels) {
		if err := cli.Delete(ctx, cfgMap); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// current gets the ConfigMap for the provided cfg from the api server.
func current(ctx context.Context, cli client.Client, cfg *configMapParams) (*corev1.ConfigMap, error) {
	current := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Namespace: cfg.Namespace,
		Name:      cfg.Name,
	}
	err := cli.Get(ctx, key, current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

// desired generates the desired ConfigMap for the given cfg.
func desired(cfg *configMapParams) (*corev1.ConfigMap, error) {
	cfgFile := new(bytes.Buffer)
	if err := sesameConfigMapTemplate.Execute(cfgFile, cfg.Sesame); err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    cfg.Labels,
		},
		Data: map[string]string{
			"sesame.yaml": cfgFile.String(),
		},
	}

	return cm, nil
}

// create creates a ConfigMap resource for the provided cm.
func create(ctx context.Context, cli client.Client, cm *corev1.ConfigMap) error {
	if err := cli.Create(ctx, cm); err != nil {
		return fmt.Errorf("failed to create configmap %s/%s: %w", cm.Namespace, cm.Name, err)
	}
	return nil
}

// updateIfNeeded updates a ConfigMap if current does not match desired,
// using cfg to verify the existence of owner labels.
func updateIfNeeded(ctx context.Context, cli client.Client, cfg *configMapParams, current, desired *corev1.ConfigMap) error {
	if labels.Exist(current, cfg.Labels) {
		changed, updated := cfgMapChanged(current, desired)
		if !changed {
			return nil
		}

		if err := cli.Update(ctx, updated); err != nil {
			return fmt.Errorf("failed to update configmap: %w", err)
		}
	}
	return nil
}

// cfgMapChanged compares current and expected returning true and an
// updated ConfigMap if they don't match.
func cfgMapChanged(current, expected *corev1.ConfigMap) (bool, *corev1.ConfigMap) {
	changed := false
	updated := current.DeepCopy()

	if !apiequality.Semantic.DeepEqual(current.Data, expected.Data) {
		changed = true
		updated.Data = expected.Data
	}

	return changed, updated
}
