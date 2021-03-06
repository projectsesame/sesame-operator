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

//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/internal/config"
	objsesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"

	core_v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const timeout = 3 * time.Minute

var (
	// kclient is the Kubernetes client used for e2e tests.
	kclient client.Client
	// ctx is an empty context used for client calls.
	ctx = context.TODO()
	// operatorName is the name of the operator.
	operatorName = "sesame-operator"
	// operatorNs is the name of the operator's namespace.
	operatorNs = "sesame-operator"
	// specNs is the spec.namespace.name of a Sesame.
	specNs = "projectsesame"
	// testAppName is the name of the application used for e2e testing.
	testAppName = "kuard"
	// testAppImage is the image used by the e2e test application.
	testAppImage = "gcr.io/kuar-demo/kuard-amd64:1"
	// testAppReplicas is the number of replicas used for the e2e test application's
	// deployment.
	testAppReplicas = 3
	// isKind determines if tests should be tuned to run in a kind cluster.
	isKind = true
)

func TestMain(m *testing.M) {
	cl, err := NewK8sClient()
	if err != nil {
		os.Exit(1)
	}
	kclient = cl

	isKind, err = isKindCluster(ctx, kclient)
	if err != nil {
		os.Exit(1)
	}

	if isKind {
		if err := labelWorkerNodes(ctx, kclient); err != nil {
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

func TestOperatorDeploymentAvailable(t *testing.T) {
	t.Helper()
	if err := WaitForDeploymentAvailable(kclient, operatorName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected conditions for deployment %s/%s: %v", operatorNs, operatorName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", operatorNs, operatorName)
}

func TestDefaultSesame(t *testing.T) {
	testName := "test-default-sesame"
	cfg := objsesame.Config{
		Name:        testName,
		Namespace:   operatorNs,
		SpecNs:      specNs,
		RemoveNs:    false,
		NetworkType: operatorv1alpha1.LoadBalancerServicePublishingType,
	}
	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	if isKind {
		svcName := "envoy"
		if err := updateLbSvcIPAndNodePorts(ctx, kclient, timeout, cfg.SpecNs, svcName); err != nil {
			t.Fatalf("failed to update service %s/%s: %v", cfg.SpecNs, svcName, err)
		}
		t.Logf("updated service %s/%s loadbalancer IP and nodeports", cfg.SpecNs, svcName)
	}

	if err := WaitForSesameAvailable(kclient, testName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", operatorNs, testName)

	// Create a sample workload for e2e testing.
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewIngress(kclient, appName, cfg.SpecNs, appName, 80); err != nil {
		t.Fatalf("failed to create ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created ingress %s/%s", cfg.SpecNs, appName)

	// testURL is the url used to test e2e functionality.
	testURL := "http://local.projectsesame.io/"

	if isKind {
		if err := WaitForHTTPResponse(testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	} else {
		lb, err := waitForIngressLB(ctx, kclient, timeout, cfg.SpecNs, appName)
		if err != nil {
			t.Fatalf("failed to get ingress %s/%s load-balancer: %v", cfg.SpecNs, appName, err)
		}
		t.Logf("ingress %s/%s has load-balancer %s", cfg.SpecNs, appName, lb)
		testURL = fmt.Sprintf("http://%s/", lb)

		// Curl the ingress from a client pod.
		cliName := "test-client"
		if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	}

	// Ensure the default sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, testName, operatorNs); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("deleted sesame %s/%s", operatorNs, testName)

	// Ensure the envoy service is cleaned up automatically.
	if err := waitForServiceDeletion(ctx, kclient, timeout, cfg.SpecNs, "envoy"); err != nil {
		t.Fatalf("failed to delete envoy service %s/envoy: %v", cfg.SpecNs, err)
	}
	t.Logf("cleaned up envoy service %s/envoy", cfg.SpecNs)

	// Delete the operand namespace since sesame.spec.namespace.removeOnDeletion
	// defaults to false.
	if err := DeleteNamespace(kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("observed the deletion of namespace %s", cfg.SpecNs)
}

func TestSesameNodePortService(t *testing.T) {
	testName := "test-nodeport-sesame"
	cfg := objsesame.Config{
		Name:        testName,
		Namespace:   operatorNs,
		SpecNs:      fmt.Sprintf("%s-nodeport", specNs),
		RemoveNs:    false,
		NetworkType: operatorv1alpha1.NodePortServicePublishingType,
		NodePorts:   objsesame.MakeNodePorts(map[string]int{"http": 30080, "https": 30443}),
	}
	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	if err := WaitForSesameAvailable(kclient, cntr.Name, cntr.Namespace); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", cntr.Namespace, cntr.Name, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", cntr.Namespace, cntr.Name)

	// Create a sample workload for e2e testing.
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewIngress(kclient, appName, cfg.SpecNs, appName, 80); err != nil {
		t.Fatalf("failed to create ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created ingress %s/%s", cfg.SpecNs, appName)

	// testURL is the url used to test e2e functionality.
	testURL := "http://local.projectsesame.io/"

	var ip string
	if isKind {
		if err := WaitForHTTPResponse(testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	} else {
		// Get the IP of a worker node to test the nodeport service.
		ip, err = getWorkerNodeIP(ctx, kclient)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("using worker node ip %s", ip)

		// Curl the ingress from a client pod.
		testURL = fmt.Sprintf("http://%s:30080/", ip)
		cliName := "test-client"
		if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	}

	// update the sesame nodeports. Note that kind is configured to map port 81>30081 and 444>30444.
	key := types.NamespacedName{
		Namespace: cfg.Namespace,
		Name:      cfg.Name,
	}
	if err := kclient.Get(ctx, key, cntr); err != nil {
		t.Fatalf("failed to get sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}

	cntr.Spec.NetworkPublishing.Envoy.NodePorts = objsesame.MakeNodePorts(map[string]int{"http": 30081, "https": 30444})
	if err := kclient.Update(ctx, cntr); err != nil {
		t.Fatalf("failed to update sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("updated sesame %s/%s", cfg.Namespace, cfg.Name)

	// Update the kuard service port.
	svc := &core_v1.Service{}
	key = types.NamespacedName{
		Namespace: cfg.SpecNs,
		Name:      appName,
	}
	if err := kclient.Get(ctx, key, svc); err != nil {
		t.Fatalf("failed to get service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	svc.Spec.Ports[0].Port = int32(81)
	if err := kclient.Update(ctx, svc); err != nil {
		t.Fatalf("failed to update service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("updated service %s/%s", cfg.SpecNs, appName)

	// Update the kuard ingress port.
	ing := &networkingv1.Ingress{}
	key = types.NamespacedName{
		Namespace: cfg.SpecNs,
		Name:      appName,
	}
	if err := kclient.Get(ctx, key, ing); err != nil {
		t.Fatalf("failed to get ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	ing.Spec.DefaultBackend.Service.Port.Number = int32(81)
	if err := kclient.Update(ctx, ing); err != nil {
		t.Fatalf("failed to update ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("updated ingress %s/%s", cfg.SpecNs, appName)

	// Update the testURL port.
	testURL = "http://local.projectsesame.io:81/"

	if isKind {
		if err := WaitForHTTPResponse(testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	} else {
		// Curl the ingress from a client pod.
		testURL = fmt.Sprintf("http://%s:30081/", ip)
		cliName := "test-client"
		if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	}

	// Ensure the default sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, cfg.Name, cfg.Namespace); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("deleted sesame %s/%s", cfg.Namespace, cfg.Name)

	// Ensure the envoy service is cleaned up automatically.
	if err := waitForServiceDeletion(ctx, kclient, timeout, cfg.SpecNs, "envoy"); err != nil {
		t.Fatalf("failed to delete envoy service %s/envoy: %v", cfg.SpecNs, err)
	}
	t.Logf("cleaned up envoy service %s/envoy", cfg.SpecNs)

	// Delete the operand namespace since sesame.spec.namespace.removeOnDeletion
	// defaults to false.
	if err := DeleteNamespace(kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("observed the deletion of namespace %s", cfg.SpecNs)
}

func TestSesameClusterIPService(t *testing.T) {
	testName := "test-clusterip-sesame"
	cfg := objsesame.Config{
		Name:        testName,
		Namespace:   operatorNs,
		SpecNs:      fmt.Sprintf("%s-clusterip", specNs),
		RemoveNs:    false,
		NetworkType: operatorv1alpha1.ClusterIPServicePublishingType,
	}
	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	if err := WaitForSesameAvailable(kclient, cntr.Name, cntr.Namespace); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", cntr.Namespace, cntr.Name, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", cntr.Namespace, cntr.Name)

	// Create a sample workload for e2e testing.
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewIngress(kclient, appName, cfg.SpecNs, appName, 80); err != nil {
		t.Fatalf("failed to create ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created ingress %s/%s", cfg.SpecNs, appName)

	// Get the Envoy ClusterIP to curl.
	svcName := "envoy"
	ip, err := envoyClusterIP(ctx, kclient, cfg.SpecNs, svcName)
	if err != nil {
		t.Fatalf("failed to get clusterIP for service %s/%s: %v", cfg.SpecNs, svcName, err)
	}

	// Curl the ingress from a client pod.
	testURL := fmt.Sprintf("http://%s/", ip)
	cliName := "test-client"
	if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
		t.Fatalf("failed to receive http response for %q: %v", testURL, err)
	}
	t.Logf("received http response for %q", testURL)

	// Ensure the default sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, testName, operatorNs); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("deleted sesame %s/%s", operatorNs, testName)

	// Ensure the envoy service is cleaned up automatically.
	if err := waitForServiceDeletion(ctx, kclient, timeout, cfg.SpecNs, "envoy"); err != nil {
		t.Fatalf("failed to delete envoy service %s/envoy: %v", cfg.SpecNs, err)
	}
	t.Logf("cleaned up envoy service %s/envoy", cfg.SpecNs)

	// Delete the operand namespace since sesame.spec.namespace.removeOnDeletion
	// defaults to false.
	if err := DeleteNamespace(kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("observed the deletion of namespace %s", cfg.SpecNs)
}

// TestSesameSpec tests some spec changes such as:
// - Enable RemoveNs.
// - Initial replicas to 4.
// - Decrease replicas to 3.
func TestSesameSpec(t *testing.T) {
	testName := "test-user-sesame"
	cfg := objsesame.Config{
		Name:        testName,
		Namespace:   operatorNs,
		SpecNs:      fmt.Sprintf("%s-Sesamespec", specNs),
		RemoveNs:    true,
		Replicas:    4,
		NetworkType: operatorv1alpha1.NodePortServicePublishingType,
		NodePorts:   objsesame.MakeNodePorts(map[string]int{"http": 30080, "https": 30443}),
	}
	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	if err := WaitForSesameAvailable(kclient, testName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", operatorNs, testName)

	// Create a sample workload for e2e testing.
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	cfg.Replicas = 3
	if _, err := updateSesame(ctx, kclient, cfg); err != nil {
		t.Fatalf("failed to update sesame %s/%s: %v", operatorNs, testName, err)
	}
	if err := WaitForSesameAvailable(kclient, testName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", operatorNs, testName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewIngress(kclient, appName, cfg.SpecNs, appName, 80); err != nil {
		t.Fatalf("failed to create ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created ingress %s/%s", cfg.SpecNs, appName)

	// testURL is the url used to test e2e functionality.
	testURL := "http://local.projectsesame.io/"

	if isKind {
		if err := WaitForHTTPResponse(testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	} else {
		// Get the IP of a worker node to test the nodeport service.
		ip, err := getWorkerNodeIP(ctx, kclient)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("using worker node ip %s", ip)

		// Curl the ingress from the client pod.
		testURL = fmt.Sprintf("http://%s:30080/", ip)
		cliName := "test-client"
		if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	}

	// Ensure the default sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, testName, operatorNs); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("deleted sesame %s/%s", operatorNs, testName)

	// Verify the user-defined namespace was removed by the operator.
	if err := waitForSpecNsDeletion(ctx, kclient, timeout, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe the deletion of namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("observed the deletion of namespace %s", cfg.SpecNs)
}

func TestMultipleSesames(t *testing.T) {
	testNames := []string{"test-user-sesame", "test-user-sesame-2"}
	for _, testName := range testNames {
		cfg := objsesame.Config{
			Name:        testName,
			Namespace:   operatorNs,
			SpecNs:      fmt.Sprintf("%s-ns", testName),
			RemoveNs:    true,
			NetworkType: operatorv1alpha1.LoadBalancerServicePublishingType,
		}
		cntr, err := newSesame(ctx, kclient, cfg)
		if err != nil {
			t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, testName, err)
		}
		t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

		if err := WaitForSesameAvailable(kclient, testName, operatorNs); err != nil {
			t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", operatorNs, testName, err)
		}
		t.Logf("observed expected status conditions for sesame %s/%s", operatorNs, testName)
	}

	// Ensure the default sesame can be deleted and clean-up.
	for _, testName := range testNames {
		if err := deleteSesame(ctx, kclient, timeout, testName, operatorNs); err != nil {
			t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, testName, err)
		}
		t.Logf("deleted sesame %s/%s", operatorNs, testName)

		// Verify the user-defined namespace was removed by the operator.
		ns := fmt.Sprintf("%s-ns", testName)
		if err := waitForSpecNsDeletion(ctx, kclient, timeout, ns); err != nil {
			t.Fatalf("failed to observe the deletion of namespace %s: %v", ns, err)
		}
		t.Logf("observed the deletion of namespace %s", ns)
	}
}

func TestGateway(t *testing.T) {
	testName := "test-gateway"
	SesameName := fmt.Sprintf("%s-sesame", testName)
	gwClassName := "test-gateway-class"
	gwControllerName := "projectsesame.io/" + gwClassName
	cfg := objsesame.Config{
		Name:                  SesameName,
		Namespace:             operatorNs,
		SpecNs:                fmt.Sprintf("%s-gateway", specNs),
		NetworkType:           operatorv1alpha1.NodePortServicePublishingType,
		NodePorts:             objsesame.MakeNodePorts(map[string]int{"http": 30080, "https": 30443}),
		GatewayControllerName: &gwControllerName,
	}

	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, SesameName, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	// The sesame should now report available.
	if err := WaitForSesameAvailable(kclient, SesameName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", cfg.Namespace, cfg.Name)

	if err := newGatewayClass(ctx, kclient, gwClassName, gwControllerName); err != nil {
		t.Fatalf("failed to create gatewayclass %s: %v", gwClassName, err)
	}
	t.Logf("created gatewayclass %s", gwClassName)

	// Create the gateway namespace if it doesn't exist.
	if err := newNs(ctx, kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to create namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("created namespace %s", cfg.SpecNs)

	// Create the gateway.
	gwName := "sesame"
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := newGateway(ctx, kclient, cfg.SpecNs, gwName, gwClassName); err != nil {
		t.Fatalf("failed to create gateway %s/%s: %v", cfg.SpecNs, gwName, err)
	}
	t.Logf("created gateway %s/%s", cfg.SpecNs, gwName)

	// Create a sample workload for e2e testing.
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewHTTPRoute(kclient, appName, cfg.SpecNs, appName, "app", appName, "local.projectsesame.io", gwName, cfg.SpecNs, int32(80)); err != nil {
		t.Fatalf("failed to create httproute %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created httproute %s/%s", cfg.SpecNs, appName)

	// testURL is the url used to test e2e functionality.
	testURL := "http://local.projectsesame.io/"

	if isKind {
		if err := WaitForHTTPResponse(testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	} else {
		// Get the IP of a worker node to test the nodeport service.
		ip, err := getWorkerNodeIP(ctx, kclient)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("using worker node ip %s", ip)

		// Curl the httproute from the client pod.
		testURL = fmt.Sprintf("http://%s:30080/", ip)
		cliName := "test-client"
		if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
			t.Fatalf("failed to receive http response for %q: %v", testURL, err)
		}
		t.Logf("received http response for %q", testURL)
	}

	// Ensure the sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, SesameName, operatorNs); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, SesameName, err)
	}
	// Ensure the envoy service is cleaned up automatically.
	if err := waitForServiceDeletion(ctx, kclient, timeout, cfg.SpecNs, "envoy"); err != nil {
		t.Fatalf("failed to delete envoy service %s/envoy: %v", cfg.SpecNs, err)
	}
	t.Logf("cleaned up envoy service %s/envoy", cfg.SpecNs)

	// Ensure the gateway can be deleted and clean-up.
	if err := deleteGateway(ctx, kclient, timeout, gwName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete gateway %s/%s: %v", cfg.SpecNs, gwName, err)
	}

	// Ensure the gatewayclass can be deleted and clean-up.
	if err := deleteGatewayClass(ctx, kclient, timeout, gwClassName); err != nil {
		t.Fatalf("failed to delete gatewayclass %s: %v", gwClassName, err)
	}

	// Delete the operand namespace since sesame.spec.namespace.removeOnDeletion
	// defaults to false.
	if err := DeleteNamespace(kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("observed the deletion of namespace %s", cfg.SpecNs)
}

func TestMultipleSesamesGateway(t *testing.T) {
	tests := []*struct {
		name        string
		gwName      string
		gwClassName string
		cfg         objsesame.Config
	}{
		{name: "test-mult-gw-1"},
		{name: "test-mult-gw-2"},
	}
	for i, test := range tests {
		test.gwName = test.name + "-gw"
		test.gwClassName = test.name + "-gc"
		gwControllerName := "projectsesame.io/" + test.gwClassName
		test.cfg = objsesame.Config{
			Name:                  test.name,
			Namespace:             operatorNs,
			SpecNs:                test.name,
			RemoveNs:              true,
			NetworkType:           operatorv1alpha1.NodePortServicePublishingType,
			NodePorts:             objsesame.MakeNodePorts(map[string]int{"http": 30080 + i, "https": 30443 + i}),
			GatewayControllerName: &gwControllerName,
		}

		cntr, err := newSesame(ctx, kclient, test.cfg)
		if err != nil {
			t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, test.name, err)
		}
		t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

		if err := WaitForSesameAvailable(kclient, test.name, operatorNs); err != nil {
			t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", operatorNs, test.name, err)
		}
		t.Logf("observed expected status conditions for sesame %s/%s", operatorNs, test.name)

		if err := newGatewayClass(ctx, kclient, test.gwClassName, gwControllerName); err != nil {
			t.Fatalf("failed to create gatewayclass %s: %v", test.gwClassName, err)
		}
		t.Logf("created gatewayclass %s", test.gwClassName)

		// Create the gateway namespace if it doesn't exist.
		if err := newNs(ctx, kclient, test.cfg.SpecNs); err != nil {
			t.Fatalf("failed to create namespace %s: %v", test.cfg.SpecNs, err)
		}
		t.Logf("created namespace %s", test.cfg.SpecNs)

		appName := fmt.Sprintf("%s-%s", testAppName, test.name)
		if err := newGateway(ctx, kclient, test.cfg.SpecNs, test.gwName, test.gwClassName); err != nil {
			t.Fatalf("failed to create gateway %s/%s: %v", test.cfg.SpecNs, test.gwName, err)
		}
		t.Logf("created gateway %s/%s", test.cfg.SpecNs, test.gwName)

		// Create a sample workload for e2e testing.
		if err := NewDeployment(kclient, appName, test.cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
			t.Fatalf("failed to create deployment %s/%s: %v", test.cfg.SpecNs, appName, err)
		}
		t.Logf("created deployment %s/%s", test.cfg.SpecNs, appName)

		if err := WaitForDeploymentAvailable(kclient, appName, test.cfg.SpecNs); err != nil {
			t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", test.cfg.SpecNs, appName, err)
		}
		t.Logf("observed expected status conditions for deployment %s/%s", test.cfg.SpecNs, appName)

		if err := NewClusterIPService(kclient, appName, test.cfg.SpecNs, 80, 8080); err != nil {
			t.Fatalf("failed to create service %s/%s: %v", test.cfg.SpecNs, appName, err)
		}
		t.Logf("created service %s/%s", test.cfg.SpecNs, appName)

		if err := NewHTTPRoute(kclient, appName, test.cfg.SpecNs, appName, "app", appName, "local.projectsesame.io", test.gwName, test.cfg.SpecNs, int32(80)); err != nil {
			t.Fatalf("failed to create httproute %s/%s: %v", test.cfg.SpecNs, appName, err)
		}
		t.Logf("created httproute %s/%s", test.cfg.SpecNs, appName)
	}

	for i, test := range tests {
		// Check routability to route in each Gateway.
		testURL := fmt.Sprintf("http://local.projectsesame.io:%d", 80+i)
		if isKind {
			if err := WaitForHTTPResponse(testURL, timeout); err != nil {
				t.Fatalf("failed to receive http response for %q: %v", testURL, err)
			}
			t.Logf("received http response for %q", testURL)
		} else {
			// Get the IP of a worker node to test the nodeport service.
			ip, err := getWorkerNodeIP(ctx, kclient)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("using worker node ip %s", ip)

			// Curl the ingress from the client pod.
			testURL = fmt.Sprintf("http://%s:%d", ip, 30080+i)
			cliName := "test-client"
			if err := podWaitForHTTPResponse(ctx, kclient, test.cfg.SpecNs, cliName, testURL, timeout); err != nil {
				t.Fatalf("failed to receive http response for %q: %v", testURL, err)
			}
			t.Logf("received http response for %q", testURL)
		}
	}

	for _, test := range tests {
		if err := deleteSesame(ctx, kclient, timeout, test.name, operatorNs); err != nil {
			t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, test.name, err)
		}

		if err := deleteGateway(ctx, kclient, timeout, test.gwName, test.cfg.SpecNs); err != nil {
			t.Fatalf("failed to delete gateway %s/%s: %v", test.cfg.SpecNs, test.gwName, err)
		}

		if err := deleteGatewayClass(ctx, kclient, timeout, test.gwClassName); err != nil {
			t.Fatalf("failed to delete gatewayclass %s: %v", test.gwClassName, err)
		}

		if err := DeleteNamespace(kclient, test.cfg.SpecNs); err != nil {
			t.Fatalf("failed to delete namespace %s: %v", test.cfg.SpecNs, err)
		}
		t.Logf("observed the deletion of namespace %s", test.cfg.SpecNs)
	}
}

func TestGatewayClusterIP(t *testing.T) {
	testName := "test-clusterip-gateway"
	SesameName := fmt.Sprintf("%s-sesame", testName)
	gwClassName := "test-clusterip-gateway-class"
	gwControllerName := "projectsesame.io/" + gwClassName
	cfg := objsesame.Config{
		Name:                  SesameName,
		Namespace:             operatorNs,
		SpecNs:                fmt.Sprintf("%s-gateway-clusterip", specNs),
		NetworkType:           operatorv1alpha1.ClusterIPServicePublishingType,
		GatewayControllerName: &gwControllerName,
	}

	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", operatorNs, SesameName, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	// The sesame should now report available.
	if err := WaitForSesameAvailable(kclient, SesameName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", operatorNs, testName, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", operatorNs, testName)

	if err := newGatewayClass(ctx, kclient, gwClassName, gwControllerName); err != nil {
		t.Fatalf("failed to create gatewayclass %s: %v", gwClassName, err)
	}
	t.Logf("created gatewayclass %s", gwClassName)

	// Create the gateway namespace if it doesn't exist.
	if err := newNs(ctx, kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to create namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("created namespace %s", cfg.SpecNs)

	// Create the gateway. The gateway must be projectsesame/sesame until the following issue is fixed:
	// https://github.com/projectsesame/sesame-operator/issues/241
	gwName := "sesame"
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := newGateway(ctx, kclient, cfg.SpecNs, gwName, gwClassName); err != nil {
		t.Fatalf("failed to create gateway %s/%s: %v", cfg.SpecNs, gwName, err)
	}
	t.Logf("created gateway %s/%s", cfg.SpecNs, gwName)

	// Create a sample workload for e2e testing.
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewHTTPRoute(kclient, appName, cfg.SpecNs, appName, "app", appName, "local.projectsesame.io", gwName, cfg.SpecNs, int32(80)); err != nil {
		t.Fatalf("failed to create httproute %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created httproute %s/%s", cfg.SpecNs, appName)

	// Get the Envoy ClusterIP to curl.
	svcName := "envoy"
	ip, err := envoyClusterIP(ctx, kclient, cfg.SpecNs, svcName)
	if err != nil {
		t.Fatalf("failed to get clusterIP for service %s/%s: %v", cfg.SpecNs, svcName, err)
	}

	// Curl the httproute from the client pod.
	testURL := fmt.Sprintf("http://%s/", ip)
	cliName := "test-client"
	if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
		t.Fatalf("failed to receive http response for %q: %v", testURL, err)
	}
	t.Logf("received http response for %q", testURL)

	// Ensure the sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, SesameName, operatorNs); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, SesameName, err)
	}

	// Ensure the envoy service is cleaned up automatically.
	if err := waitForServiceDeletion(ctx, kclient, timeout, cfg.SpecNs, "envoy"); err != nil {
		t.Fatalf("failed to delete envoy service %s/envoy: %v", cfg.SpecNs, err)
	}
	t.Logf("cleaned up envoy service %s/envoy", cfg.SpecNs)

	// Ensure the gateway can be deleted and clean-up.
	if err := deleteGateway(ctx, kclient, timeout, gwName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete gateway %s/%s: %v", cfg.SpecNs, gwName, err)
	}

	// Ensure the gatewayclass can be deleted and clean-up.
	if err := deleteGatewayClass(ctx, kclient, timeout, gwClassName); err != nil {
		t.Fatalf("failed to delete gatewayclass %s: %v", gwClassName, err)
	}

	// Delete the operand namespace since sesame.spec.namespace.removeOnDeletion
	// defaults to false.
	if err := DeleteNamespace(kclient, cfg.SpecNs); err != nil {
		t.Fatalf("failed to delete namespace %s: %v", cfg.SpecNs, err)
	}
	t.Logf("observed the deletion of namespace %s", cfg.SpecNs)
}

// TestOperatorUpgrade tests an instance of the Sesame custom resource while
// upgrading the operator from release "latest" to the current version/branch.
func TestOperatorUpgrade(t *testing.T) {
	// Skip this test until the v1.20 release is out, since it does not
	// properly handle the v1alpha1->v1alpha2 changes to Gateway API CRDs & RBAC.
	// TODO(1.20) delete the following 5 lines that are being used to disable
	// the test while keeping the linter happy.
	_ = setDeploymentImage
	_ = waitForImage
	_ = getDeploymentImage
	_ = getDeployment
	t.SkipNow()

	// Get the current image to use for upgrade testing.
	current, err := getDeploymentImage(ctx, kclient, operatorName, operatorNs, operatorName)
	if err != nil {
		t.Fatalf("failed to get image for deployment %s/%s", operatorNs, operatorName)
	}
	// Ensure the current image is not the "latest" release.
	latest := "ghcr.io/projectsesame/sesame-operator:latest"
	if current == latest {
		t.Fatalf("unexpected image %s for deployment %s/%s", current, operatorNs, operatorName)
	}
	t.Logf("found image %s for deployment %s/%s", current, operatorNs, operatorName)

	// Set the operator image as "latest" to simulate the previous release.
	if err := setDeploymentImage(ctx, kclient, operatorName, operatorNs, operatorName, latest); err != nil {
		t.Fatalf("failed to set image %s for deployment %s/%s: %v", latest, operatorNs, operatorName, err)
	}
	t.Logf("set image %s for deployment %s/%s", latest, operatorNs, operatorName)

	// Wait for the operator container to use image "latest".
	if err := waitForImage(ctx, kclient, timeout, operatorNs, "control-plane", operatorName, operatorName, latest); err != nil {
		t.Fatalf("failed to observe image %s for deployment %s/%s: %v", latest, operatorNs, operatorName, err)
	}
	t.Logf("observed image %s for deployment %s/%s", latest, operatorNs, operatorName)

	// Ensure the operator's deployment becomes available before proceeding.
	if err := WaitForDeploymentAvailable(kclient, operatorName, operatorNs); err != nil {
		t.Fatalf("failed to observe expected conditions for deployment %s/%s: %v", operatorNs, operatorName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", operatorNs, operatorName)

	testName := "upgrade"
	SesameName := fmt.Sprintf("%s-sesame", testName)
	cfg := objsesame.Config{
		Name:        SesameName,
		Namespace:   operatorNs,
		RemoveNs:    true,
		SpecNs:      fmt.Sprintf("%s-%s", testName, specNs),
		NetworkType: operatorv1alpha1.ClusterIPServicePublishingType,
	}

	cntr, err := newSesame(ctx, kclient, cfg)
	if err != nil {
		t.Fatalf("failed to create sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("created sesame %s/%s", cntr.Namespace, cntr.Name)

	// The sesame should now report available.
	if err := WaitForSesameAvailable(kclient, cfg.Name, cfg.Namespace); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", cfg.Namespace, cfg.Name)

	// Create a sample workload for e2e testing.
	appName := fmt.Sprintf("%s-%s", testAppName, testName)
	if err := NewDeployment(kclient, appName, cfg.SpecNs, testAppImage, testAppReplicas); err != nil {
		t.Fatalf("failed to create deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created deployment %s/%s", cfg.SpecNs, appName)

	// Wait for the sample workload to become available.
	if err := WaitForDeploymentAvailable(kclient, appName, cfg.SpecNs); err != nil {
		t.Fatalf("failed to observe expected status conditions for deployment %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("observed expected status conditions for deployment %s/%s", cfg.SpecNs, appName)

	if err := NewClusterIPService(kclient, appName, cfg.SpecNs, 80, 8080); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created service %s/%s", cfg.SpecNs, appName)

	if err := NewIngress(kclient, appName, cfg.SpecNs, appName, 80); err != nil {
		t.Fatalf("failed to create ingress %s/%s: %v", cfg.SpecNs, appName, err)
	}
	t.Logf("created ingress %s/%s", cfg.SpecNs, appName)

	// Get the Envoy ClusterIP to curl.
	svcName := "envoy"
	ip, err := envoyClusterIP(ctx, kclient, cfg.SpecNs, svcName)
	if err != nil {
		t.Fatalf("failed to get clusterIP for service %s/%s: %v", cfg.SpecNs, svcName, err)
	}

	// Create the client pod to test connectivity to the sample workload.
	cliName := "test-client"
	testURL := fmt.Sprintf("http://%s/", ip)
	if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
		t.Fatalf("failed to receive http response for %q: %v", testURL, err)
	}
	t.Logf("received http response for %q", testURL)

	// Simulate an upgrade from the previous release, i.e. image "latest", to the current release.
	if err := setDeploymentImage(ctx, kclient, operatorName, operatorNs, operatorName, current); err != nil {
		t.Fatalf("failed to set image %s for deployment %s/%s: %v", latest, operatorNs, operatorName, err)
	}
	t.Logf("set image %s for deployment %s/%s", current, operatorNs, operatorName)

	// Wait for the operator container to use the updated image.
	if err := waitForImage(ctx, kclient, timeout, operatorNs, "control-plane", operatorName, operatorName, current); err != nil {
		t.Fatalf("failed to observe image %s for deployment %s/%s: %v", current, operatorNs, operatorName, err)
	}
	t.Logf("observed image %s for deployment %s/%s", current, operatorNs, operatorName)

	// Wait for the sesame containers to use the current tag.
	wantSesameImage := config.DefaultSesameImage
	if err := waitForImage(ctx, kclient, timeout, cfg.SpecNs, "app", "sesame", "sesame", wantSesameImage); err != nil {
		t.Fatalf("failed to observe image %s for deployment %s/sesame: %v", wantSesameImage, cfg.SpecNs, err)
	}
	t.Logf("observed image %s for deployment %s/sesame", wantSesameImage, cfg.SpecNs)

	// The sesame should now report available.
	if err := WaitForSesameAvailable(kclient, cfg.Name, cfg.Namespace); err != nil {
		t.Fatalf("failed to observe expected status conditions for sesame %s/%s: %v", cfg.Namespace, cfg.Name, err)
	}
	t.Logf("observed expected status conditions for sesame %s/%s", cfg.Namespace, cfg.Name)

	if err := podWaitForHTTPResponse(ctx, kclient, cfg.SpecNs, cliName, testURL, timeout); err != nil {
		t.Fatalf("failed to receive http response for %q: %v", testURL, err)
	}
	t.Logf("received http response for %q", testURL)

	// Ensure the sesame can be deleted and clean-up.
	if err := deleteSesame(ctx, kclient, timeout, SesameName, operatorNs); err != nil {
		t.Fatalf("failed to delete sesame %s/%s: %v", operatorNs, SesameName, err)
	}
}
