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

package operator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	operatorconfig "github.com/projectsesame/sesame-operator/internal/config"
	"github.com/projectsesame/sesame-operator/internal/operator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
	apps_v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	controller_runtime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	log "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	operatorNSName = "operator-ns"

	defaultWait = time.Second * 10
	defaultTick = time.Millisecond * 20
)

func TestOperator(t *testing.T) {
	log.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	opCRD := filepath.Join("..", "..", "config", "crd", "bases")
	SesameCRDs := filepath.Join("..", "..", "config", "crd", "sesame")
	gatewayCRDs := filepath.Join("..", "..", "config", "crd", "gateway")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{opCRD, SesameCRDs, gatewayCRDs},
	}
	clientConfig, err := testEnv.Start()
	require.NoError(t, err)
	require.NotNil(t, clientConfig)
	k8sClient, err := client.New(clientConfig, client.Options{Scheme: operator.GetOperatorScheme()})
	require.NoError(t, err)

	operator, err := operator.New(clientConfig, operatorconfig.New())
	require.NoError(t, err)
	operatorCtx, stopOperator := context.WithCancel(controller_runtime.SetupSignalHandler())
	go func() {
		require.NoError(t, operator.Start(operatorCtx))
	}()

	operatorNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: operatorNSName}}
	require.NoError(t, k8sClient.Create(context.Background(), operatorNS))

	// Cleanup.
	defer func() {
		require.NoError(t, k8sClient.Delete(context.Background(), operatorNS))
		stopOperator()
		require.NoError(t, testEnv.Stop())
	}()

	subtests := map[string]func(*testing.T, client.Client){
		"default fields":                                         testEnsureDefaultFields,
		"sesame object should have finalizer":                    testEnsureFinalizer,
		"namespace remove on delete works":                       testNamespaceRemoveOnDelete,
		"replicas controls number of sesame deployment replicas": testReplicas,
		"ingress class name":                                     testIngressClassName,
		"gateway controller name":                                testGatewayControllerName,
	}
	for name, subtest := range subtests {
		t.Run(name, func(t *testing.T) {
			subtest(t, k8sClient)
		})
	}
}

func testEnsureDefaultFields(t *testing.T, k8sClient client.Client) {
	basicSesame := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "basic",
			Namespace: operatorNSName,
		},
	}
	key := client.ObjectKeyFromObject(basicSesame)
	require.NoError(t, k8sClient.Create(context.Background(), basicSesame))
	defer func() {
		require.NoError(t, k8sClient.Delete(context.Background(), basicSesame))
	}()

	updatedSesame := &operatorv1alpha1.Sesame{}
	require.Eventually(t, func() bool {
		return k8sClient.Get(context.Background(), key, updatedSesame) == nil
	}, defaultWait, defaultTick)

	// This section basically just tests the kubebuilder defaults are set on
	// the object, could also be tested elsewhere.
	assert.Equal(t, operatorv1alpha1.NamespaceSpec{
		Name:             "projectsesame",
		RemoveOnDeletion: false,
	}, updatedSesame.Spec.Namespace)
	assert.Equal(t, int32(2), updatedSesame.Spec.Replicas)
	assert.Equal(t, operatorv1alpha1.NetworkPublishing{
		Envoy: operatorv1alpha1.EnvoyNetworkPublishing{
			Type: operatorv1alpha1.LoadBalancerServicePublishingType,
			LoadBalancer: operatorv1alpha1.LoadBalancerStrategy{
				ProviderParameters: operatorv1alpha1.ProviderLoadBalancerParameters{
					Type: operatorv1alpha1.AWSLoadBalancerProvider,
				},
				Scope: operatorv1alpha1.ExternalLoadBalancer,
			},
			ContainerPorts: []operatorv1alpha1.ContainerPort{
				{Name: "http", PortNumber: 8080},
				{Name: "https", PortNumber: 8443},
			},
		},
	}, updatedSesame.Spec.NetworkPublishing)

	// Below, actually test resources were created how we expect them.
	// TODO: How detailed should we get here?

	// Namespace.
	assert.Eventually(t, func() bool {
		key := client.ObjectKey{Name: "projectsesame"}
		return k8sClient.Get(context.Background(), key, &corev1.Namespace{}) == nil
	}, defaultWait, defaultTick)

	// Sesame Deployment.
	deployment := &apps_v1.Deployment{}
	require.Eventually(t, func() bool {
		key := client.ObjectKey{Namespace: "projectsesame", Name: "sesame"}
		return k8sClient.Get(context.Background(), key, deployment) == nil
	}, defaultWait, defaultTick)
	require.NotNil(t, deployment.Spec.Replicas)
	assert.Equal(t, int32(2), *deployment.Spec.Replicas)

	// Envoy Service.
	service := &corev1.Service{}
	require.Eventually(t, func() bool {
		key := client.ObjectKey{Namespace: "projectsesame", Name: "envoy"}
		return k8sClient.Get(context.Background(), key, service) == nil
	}, defaultWait, defaultTick)
	assert.Equal(t, corev1.ServiceTypeLoadBalancer, service.Spec.Type)
}

func testEnsureFinalizer(t *testing.T, k8sClient client.Client) {
	basicSesame := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "finalizers",
			Namespace: operatorNSName,
		},
		Spec: operatorv1alpha1.SesameSpec{
			Namespace: operatorv1alpha1.NamespaceSpec{
				Name: "finalizers",
			},
		},
	}
	key := client.ObjectKeyFromObject(basicSesame)
	require.NoError(t, k8sClient.Create(context.Background(), basicSesame))

	updatedSesame := &operatorv1alpha1.Sesame{}
	// Check for finalizer.
	assert.Eventually(t, func() bool {
		if err := k8sClient.Get(context.Background(), key, updatedSesame); err != nil {
			return false
		}
		return len(updatedSesame.Finalizers) == 1 &&
			updatedSesame.Finalizers[0] == "sesame.operator.projectsesame.io/finalizer"
	}, defaultWait, defaultTick)

	// Remove finalizer.
	updatedSesame.Finalizers = []string{}
	require.NoError(t, k8sClient.Update(context.Background(), updatedSesame))

	// Check finalizer is re-added.
	assert.Eventually(t, func() bool {
		if err := k8sClient.Get(context.Background(), key, updatedSesame); err != nil {
			return false
		}
		return len(updatedSesame.Finalizers) == 1 &&
			updatedSesame.Finalizers[0] == "sesame.operator.projectsesame.io/finalizer"
	}, defaultWait, defaultTick)

	// Delete Sesame and ensure object actually deleted.
	require.NoError(t, k8sClient.Delete(context.Background(), updatedSesame))
	assert.Eventually(t, func() bool {
		return errors.IsNotFound(k8sClient.Get(context.Background(), key, updatedSesame))
	}, defaultWait, defaultTick)
}

func testNamespaceRemoveOnDelete(t *testing.T, k8sClient client.Client) {
	noDeleteNS := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ns-no-delete",
			Namespace: operatorNSName,
		},
		Spec: operatorv1alpha1.SesameSpec{
			Namespace: operatorv1alpha1.NamespaceSpec{
				Name:             "no-delete",
				RemoveOnDeletion: false,
			},
		},
	}
	noDeleteNSKey := client.ObjectKey{Name: "no-delete"}
	require.NoError(t, k8sClient.Create(context.Background(), noDeleteNS))
	require.Eventually(t, func() bool {
		return k8sClient.Get(context.Background(), noDeleteNSKey, &corev1.Namespace{}) == nil
	}, defaultWait, defaultTick)

	require.NoError(t, k8sClient.Delete(context.Background(), noDeleteNS))
	assert.Never(t, func() bool {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(context.Background(), noDeleteNSKey, ns); err != nil {
			return true
		}
		return !ns.DeletionTimestamp.IsZero()
	}, defaultWait, defaultTick)

	deleteNS := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ns-delete",
			Namespace: operatorNSName,
		},
		Spec: operatorv1alpha1.SesameSpec{
			Namespace: operatorv1alpha1.NamespaceSpec{
				Name:             "delete",
				RemoveOnDeletion: true,
			},
		},
	}
	deleteNSKey := client.ObjectKey{Name: "delete"}
	require.NoError(t, k8sClient.Create(context.Background(), deleteNS))
	require.Eventually(t, func() bool {
		return k8sClient.Get(context.Background(), deleteNSKey, &corev1.Namespace{}) == nil
	}, defaultWait, defaultTick)

	require.NoError(t, k8sClient.Delete(context.Background(), deleteNS))
	assert.Eventually(t, func() bool {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(context.Background(), deleteNSKey, ns); err != nil {
			return true
		}
		return !ns.DeletionTimestamp.IsZero()
	}, defaultWait, defaultTick)
}

func testReplicas(t *testing.T, k8sClient client.Client) {
	replicas := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "replicas",
			Namespace: operatorNSName,
		},
		Spec: operatorv1alpha1.SesameSpec{
			Namespace: operatorv1alpha1.NamespaceSpec{
				Name: "replicas",
			},
			Replicas: 5,
		},
	}
	require.NoError(t, k8sClient.Create(context.Background(), replicas))
	defer func() {
		require.NoError(t, k8sClient.Delete(context.Background(), replicas))
	}()

	deployment := &apps_v1.Deployment{}
	require.Eventually(t, func() bool {
		key := client.ObjectKey{Namespace: "replicas", Name: "sesame"}
		return k8sClient.Get(context.Background(), key, deployment) == nil
	}, defaultWait, defaultTick)
	require.NotNil(t, deployment.Spec.Replicas)
	assert.Equal(t, int32(5), *deployment.Spec.Replicas)
}

func testIngressClassName(t *testing.T, k8sClient client.Client) {
	ingress := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ingress",
			Namespace: operatorNSName,
		},
		Spec: operatorv1alpha1.SesameSpec{
			Namespace: operatorv1alpha1.NamespaceSpec{
				Name: "ingress",
			},
			IngressClassName: pointer.String("some-class"),
		},
	}
	require.NoError(t, k8sClient.Create(context.Background(), ingress))
	defer func() {
		require.NoError(t, k8sClient.Delete(context.Background(), ingress))
	}()

	deployment := &apps_v1.Deployment{}
	require.Eventually(t, func() bool {
		key := client.ObjectKey{Namespace: "ingress", Name: "sesame"}
		return k8sClient.Get(context.Background(), key, deployment) == nil
	}, defaultWait, defaultTick)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	require.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, "--ingress-class-name=some-class")
}

func testGatewayControllerName(t *testing.T, k8sClient client.Client) {
	gatewaySesame := &operatorv1alpha1.Sesame{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway",
			Namespace: operatorNSName,
		},
		Spec: operatorv1alpha1.SesameSpec{
			Namespace: operatorv1alpha1.NamespaceSpec{
				Name: operatorNSName,
			},
			GatewayControllerName: pointer.String("somecontrollername"),
		},
	}
	require.NoError(t, k8sClient.Create(context.Background(), gatewaySesame))
	defer func() {
		require.NoError(t, k8sClient.Delete(context.Background(), gatewaySesame))
	}()

	configMap := &corev1.ConfigMap{}
	require.Eventually(t, func() bool {
		key := client.ObjectKey{Namespace: operatorNSName, Name: "sesame"}
		return k8sClient.Get(context.Background(), key, configMap) == nil
	}, defaultWait, defaultTick)
	require.Contains(t, configMap.Data, "sesame.yaml")

	SesameConfig := struct {
		GatewayConfig *struct {
			ControllerName string `yaml:"controllerName,omitempty"`
		} `yaml:"gateway,omitempty"`
	}{}
	require.NoError(t, yaml.Unmarshal([]byte(configMap.Data["sesame.yaml"]), &SesameConfig))
	require.NotNil(t, SesameConfig.GatewayConfig)
	assert.Equal(t, "somecontrollername", SesameConfig.GatewayConfig.ControllerName)
}
