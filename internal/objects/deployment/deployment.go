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

package deployment

import (
	"context"
	"fmt"
	"path/filepath"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/internal/equality"
	opintstr "github.com/projectsesame/sesame-operator/internal/intstr"
	objutil "github.com/projectsesame/sesame-operator/internal/objects"
	objcm "github.com/projectsesame/sesame-operator/internal/objects/configmap"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	objcfg "github.com/projectsesame/sesame-operator/internal/objects/sharedconfig"
	"github.com/projectsesame/sesame-operator/pkg/labels"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SesameDeploymentName is the name of Sesame's Deployment resource.
	// [TODO] danehans: Remove and use sesame.Name + "-sesame" when
	// https://github.com/projectsesame/Sesame/issues/2122 is fixed.
	SesameDeploymentName = "sesame"
	// SesameContainerName is the name of the Sesame container.
	SesameContainerName = "sesame"
	// SesameNsEnvVar is the name of the sesame namespace environment variable.
	SesameNsEnvVar = "Sesame_NAMESPACE"
	// SesamePodEnvVar is the name of the sesame pod name environment variable.
	SesamePodEnvVar = "POD_NAME"
	// SesameCertsVolName is the name of the sesame certificates volume.
	SesameCertsVolName = "Sesamecert"
	// SesameCertsVolMntDir is the directory name of the sesame certificates volume.
	SesameCertsVolMntDir = "certs"
	// SesameCertsSecretName is the name of the secret used as the certificate volume source.
	SesameCertsSecretName = SesameCertsVolName
	// SesameCfgVolName is the name of the sesame configuration volume.
	SesameCfgVolName = "sesame-config"
	// SesameCfgVolMntDir is the directory name of the sesame configuration volume.
	SesameCfgVolMntDir = "config"
	// SesameCfgFileName is the name of the sesame configuration file.
	SesameCfgFileName = "sesame.yaml"
	// metricsPort is the network port number of Sesame's metrics service.
	metricsPort = 8000
	// debugPort is the network port number of Sesame's debug service.
	debugPort = 6060
)

// EnsureDeployment ensures a deployment using image exists for the given sesame.
func EnsureDeployment(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, image string) error {
	desired := DesiredDeployment(Sesame, image)
	current, err := CurrentDeployment(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			if err := createDeployment(ctx, cli, desired); err != nil {
				return fmt.Errorf("failed to create deployment %s/%s: %w", desired.Namespace, desired.Name, err)
			}
			return nil
		}
	}
	differ := equality.DeploymentSelectorsDiffer(current, desired)
	if differ {
		return EnsureDeploymentDeleted(ctx, cli, Sesame)
	}
	if err := updateDeploymentIfNeeded(ctx, cli, Sesame, current, desired); err != nil {
		return fmt.Errorf("failed to update deployment %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	return nil
}

// EnsureDeploymentDeleted ensures the deployment for the provided sesame
// is deleted if Sesame owner labels exist.
func EnsureDeploymentDeleted(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	deploy, err := CurrentDeployment(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if labels.Exist(deploy, objSesame.OwnerLabels(Sesame)) {
		if err := cli.Delete(ctx, deploy); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// DesiredDeployment returns the desired deployment for the provided sesame using
// image as Sesame's container image.
func DesiredDeployment(Sesame *operatorv1alpha1.Sesame, image string) *appsv1.Deployment {
	xdsPort := objcfg.XDSPort
	args := []string{
		"serve",
		"--incluster",
		"--xds-address=0.0.0.0",
		fmt.Sprintf("--xds-port=%d", xdsPort),
		fmt.Sprintf("--sesame-cafile=%s", filepath.Join("/", SesameCertsVolMntDir, "ca.crt")),
		fmt.Sprintf("--sesame-cert-file=%s", filepath.Join("/", SesameCertsVolMntDir, "tls.crt")),
		fmt.Sprintf("--sesame-key-file=%s", filepath.Join("/", SesameCertsVolMntDir, "tls.key")),
		fmt.Sprintf("--config-path=%s", filepath.Join("/", SesameCfgVolMntDir, SesameCfgFileName)),
	}
	// Pass the insecure/secure flags to Sesame if using non-default ports.
	for _, port := range Sesame.Spec.NetworkPublishing.Envoy.ContainerPorts {
		switch {
		case port.Name == "http" && port.PortNumber != objcfg.EnvoyInsecureContainerPort:
			args = append(args, fmt.Sprintf("--envoy-service-http-port=%d", port.PortNumber))
		case port.Name == "https" && port.PortNumber != objcfg.EnvoySecureContainerPort:
			args = append(args, fmt.Sprintf("--envoy-service-https-port=%d", port.PortNumber))
		}
	}
	if Sesame.Spec.IngressClassName != nil {
		args = append(args, fmt.Sprintf("--ingress-class-name=%s", *Sesame.Spec.IngressClassName))
	}
	container := corev1.Container{
		Name:            SesameContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sesame"},
		Args:            args,
		Env: []corev1.EnvVar{
			{
				Name: SesameNsEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.namespace",
					},
				},
			},
			{
				Name: SesamePodEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.name",
					},
				},
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "xds",
				ContainerPort: xdsPort,
				Protocol:      "TCP",
			},
			{
				Name:          "metrics",
				ContainerPort: metricsPort,
				Protocol:      "TCP",
			},
			{
				Name:          "debug",
				ContainerPort: debugPort,
				Protocol:      "TCP",
			},
		},
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Scheme: corev1.URISchemeHTTP,
					Path:   "/healthz",
					Port:   intstr.IntOrString{IntVal: int32(metricsPort)},
				},
			},
			TimeoutSeconds:   int32(1),
			PeriodSeconds:    int32(10),
			SuccessThreshold: int32(1),
			FailureThreshold: int32(3),
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.IntOrString{
						IntVal: xdsPort,
					},
				},
			},
			TimeoutSeconds:      int32(1),
			InitialDelaySeconds: int32(15),
			PeriodSeconds:       int32(10),
			SuccessThreshold:    int32(1),
			FailureThreshold:    int32(3),
		},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      SesameCertsVolName,
				MountPath: filepath.Join("/", SesameCertsVolMntDir),
				ReadOnly:  true,
			},
			{
				Name:      SesameCfgVolName,
				MountPath: filepath.Join("/", SesameCfgVolMntDir),
				ReadOnly:  true,
			},
		},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: Sesame.Spec.Namespace.Name,
			Name:      SesameDeploymentName,
			Labels:    makeDeploymentLabels(Sesame),
		},
		Spec: appsv1.DeploymentSpec{
			ProgressDeadlineSeconds: pointer.Int32Ptr(int32(600)),
			Replicas:                &Sesame.Spec.Replicas,
			RevisionHistoryLimit:    pointer.Int32Ptr(int32(10)),
			// Ensure the deployment adopts only its own pods.
			Selector: SesameDeploymentPodSelector(),
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       opintstr.PointerTo(intstr.FromString("50%")),
					MaxUnavailable: opintstr.PointerTo(intstr.FromString("25%")),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					// TODO [danehans]: Remove the prometheus annotations when Sesame is updated to
					// show how the Prometheus Operator is used to scrape Sesame/Envoy metrics.
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   fmt.Sprintf("%d", metricsPort),
					},
					Labels: SesameDeploymentPodSelector().MatchLabels,
				},
				Spec: corev1.PodSpec{
					// TODO [danehans]: Readdress anti-affinity when https://github.com/projectsesame/Sesame/issues/2997
					// is resolved.
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: int32(100),
									PodAffinityTerm: corev1.PodAffinityTerm{
										TopologyKey: "kubernetes.io/hostname",
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: SesameDeploymentPodSelector().MatchLabels,
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{container},
					Volumes: []corev1.Volume{
						{
							Name: SesameCertsVolName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									DefaultMode: pointer.Int32Ptr(int32(420)),
									SecretName:  SesameCertsSecretName,
								},
							},
						},
						{
							Name: SesameCfgVolName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										// [TODO] danehans: Update to sesame.Name when
										// projectsesame/sesame/issues/2122 is fixed.
										Name: objcm.SesameConfigMapName,
									},
									Items: []corev1.KeyToPath{
										{
											Key:  SesameCfgFileName,
											Path: SesameCfgFileName,
										},
									},
									DefaultMode: pointer.Int32Ptr(int32(420)),
								},
							},
						},
					},
					DNSPolicy:                     corev1.DNSClusterFirst,
					DeprecatedServiceAccount:      objutil.SesameRbacName,
					ServiceAccountName:            objutil.SesameRbacName,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 "default-scheduler",
					SecurityContext:               objutil.NewUnprivilegedPodSecurity(),
					TerminationGracePeriodSeconds: pointer.Int64Ptr(int64(30)),
				},
			},
		},
	}

	if Sesame.SesameNodeSelectorExists() {
		deploy.Spec.Template.Spec.NodeSelector = Sesame.Spec.NodePlacement.Sesame.NodeSelector
	}

	if Sesame.SesameTolerationsExist() {
		deploy.Spec.Template.Spec.Tolerations = Sesame.Spec.NodePlacement.Sesame.Tolerations
	}

	return deploy
}

// CurrentDeployment returns the Deployment resource for the provided sesame.
func CurrentDeployment(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) (*appsv1.Deployment, error) {
	deploy := &appsv1.Deployment{}
	key := types.NamespacedName{
		Namespace: Sesame.Spec.Namespace.Name,
		Name:      SesameDeploymentName,
	}
	if err := cli.Get(ctx, key, deploy); err != nil {
		return nil, err
	}
	return deploy, nil
}

// createDeployment creates a Deployment resource for the provided deploy.
func createDeployment(ctx context.Context, cli client.Client, deploy *appsv1.Deployment) error {
	if err := cli.Create(ctx, deploy); err != nil {
		return fmt.Errorf("failed to create deployment %s/%s: %w", deploy.Namespace, deploy.Name, err)
	}
	return nil
}

// updateDeploymentIfNeeded updates a Deployment if current does not match desired,
// using sesame to verify the existence of owner labels.
func updateDeploymentIfNeeded(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, current, desired *appsv1.Deployment) error {
	if labels.Exist(current, objSesame.OwnerLabels(Sesame)) {
		deploy, updated := equality.DeploymentConfigChanged(current, desired)
		if updated {
			if err := cli.Update(ctx, deploy); err != nil {
				return fmt.Errorf("failed to update deployment %s/%s: %w", deploy.Namespace, deploy.Name, err)
			}
		}
	}
	return nil
}

// makeDeploymentLabels returns labels for a Sesame deployment.
func makeDeploymentLabels(Sesame *operatorv1alpha1.Sesame) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "sesame",
		"app.kubernetes.io/instance":   Sesame.Name,
		"app.kubernetes.io/component":  "ingress-controller",
		"app.kubernetes.io/managed-by": "sesame-operator",
		// Associate the deployment with the provided sesame.
		operatorv1alpha1.OwningSesameNameLabel: Sesame.Name,
		operatorv1alpha1.OwningSesameNsLabel:   Sesame.Namespace,
	}
}

// SesameDeploymentPodSelector returns a label selector using "app: sesame" as the
// key/value pair.
//
// TODO [danehans]: Update to use "sesame.operator.projectsesame.io/deployment-sesame"
// when https://github.com/projectsesame/Sesame/issues/1821 is fixed.
func SesameDeploymentPodSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app": "sesame",
		},
	}
}
