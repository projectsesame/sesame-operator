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

package daemonset

import (
	"context"
	"fmt"
	"path/filepath"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/internal/equality"
	opintstr "github.com/projectsesame/sesame-operator/internal/intstr"
	objutil "github.com/projectsesame/sesame-operator/internal/objects"
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
	// envoyDaemonSetName is the name of Envoy's DaemonSet resource.
	// [TODO] danehans: Remove and use sesame.Name + "-envoy" when
	// https://github.com/projectsesame/Sesame/issues/2122 is fixed.
	envoyDaemonSetName = "envoy"
	// EnvoyContainerName is the name of the Envoy container.
	EnvoyContainerName = "envoy"
	// ShutdownContainerName is the name of the Shutdown Manager container.
	ShutdownContainerName = "shutdown-manager"
	// envoyInitContainerName is the name of the Envoy init container.
	envoyInitContainerName = "envoy-initconfig"
	// envoyNsEnvVar is the name of the sesame namespace environment variable.
	envoyNsEnvVar = "Sesame_NAMESPACE"
	// envoyPodEnvVar is the name of the Envoy pod name environment variable.
	envoyPodEnvVar = "ENVOY_POD_NAME"
	// envoyCertsVolName is the name of the sesame certificates volume.
	envoyCertsVolName = "envoycert"
	// envoyCertsVolMntDir is the directory name of the Envoy certificates volume.
	envoyCertsVolMntDir = "certs"
	// envoyCertsSecretName is the name of the secret used as the certificate volume source.
	envoyCertsSecretName = envoyCertsVolName
	// envoyCfgVolName is the name of the Envoy configuration volume.
	envoyCfgVolName = "envoy-config"
	// envoyCfgVolMntDir is the directory name of the Envoy configuration volume.
	envoyCfgVolMntDir = "config"
	// envoyAdminVolName is the name of the Envoy admin volume.
	envoyAdminVolName = "envoy-admin"
	// envoyAdminVolMntDir is the directory name of the Envoy admin volume.
	envoyAdminVolMntDir = "admin"
	// envoyCfgFileName is the name of the Envoy configuration file.
	envoyCfgFileName = "envoy.json"
	// xdsResourceVersion is the version of the Envoy xdS resource types.
	xdsResourceVersion = "v3"
)

// EnsureDaemonSet ensures a DaemonSet exists for the given sesame.
func EnsureDaemonSet(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, SesameImage, envoyImage string) error {
	desired := DesiredDaemonSet(Sesame, SesameImage, envoyImage)
	current, err := CurrentDaemonSet(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return createDaemonSet(ctx, cli, desired)
		}
		return fmt.Errorf("failed to get daemonset %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	differ := equality.DaemonSetSelectorsDiffer(current, desired)
	if differ {
		return EnsureDaemonSetDeleted(ctx, cli, Sesame)
	}
	if err := updateDaemonSetIfNeeded(ctx, cli, Sesame, current, desired); err != nil {
		return fmt.Errorf("failed to update daemonset for sesame %s/%s: %w", Sesame.Namespace, Sesame.Name, err)
	}
	return nil
}

// EnsureDaemonSetDeleted ensures the DaemonSet for the provided sesame is deleted
// if Sesame owner labels exist.
func EnsureDaemonSetDeleted(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	ds, err := CurrentDaemonSet(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if labels.Exist(ds, objSesame.OwnerLabels(Sesame)) {
		if err := cli.Delete(ctx, ds); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// DesiredDaemonSet returns the desired DaemonSet for the provided sesame using
// SesameImage as the shutdown-manager/envoy-initconfig container images and
// envoyImage as Envoy's container image.
func DesiredDaemonSet(Sesame *operatorv1alpha1.Sesame, SesameImage, envoyImage string) *appsv1.DaemonSet {
	labels := map[string]string{
		"app.kubernetes.io/name":       "sesame",
		"app.kubernetes.io/instance":   Sesame.Name,
		"app.kubernetes.io/component":  "ingress-controller",
		"app.kubernetes.io/managed-by": "sesame-operator",
		// Associate the daemonset with the provided sesame.
		operatorv1alpha1.OwningSesameNsLabel:   Sesame.Namespace,
		operatorv1alpha1.OwningSesameNameLabel: Sesame.Name,
	}

	var ports []corev1.ContainerPort
	for _, port := range Sesame.Spec.NetworkPublishing.Envoy.ContainerPorts {
		p := corev1.ContainerPort{
			Name:          port.Name,
			ContainerPort: port.PortNumber,
			Protocol:      corev1.ProtocolTCP,
		}
		ports = append(ports, p)
	}

	containers := []corev1.Container{
		{
			Name:            ShutdownContainerName,
			Image:           SesameImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{
				"/bin/sesame",
			},
			Args: []string{
				"envoy",
				"shutdown-manager",
			},
			LivenessProbe: &corev1.Probe{
				FailureThreshold: int32(3),
				Handler: corev1.Handler{
					HTTPGet: &corev1.HTTPGetAction{
						Scheme: corev1.URISchemeHTTP,
						Path:   "/healthz",
						Port:   intstr.IntOrString{IntVal: int32(8090)},
					},
				},
				InitialDelaySeconds: int32(3),
				PeriodSeconds:       int32(10),
				SuccessThreshold:    int32(1),
				TimeoutSeconds:      int32(1),
			},
			Lifecycle: &corev1.Lifecycle{
				PreStop: &corev1.Handler{
					Exec: &corev1.ExecAction{
						Command: []string{"/bin/sesame", "envoy", "shutdown"},
					},
				},
			},
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			TerminationMessagePath:   "/dev/termination-log",
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      envoyAdminVolName,
					MountPath: filepath.Join("/", envoyAdminVolMntDir),
				},
			},
		},
		{
			Name:            EnvoyContainerName,
			Image:           envoyImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{
				"envoy",
			},
			Args: []string{
				"-c",
				filepath.Join("/", envoyCfgVolMntDir, envoyCfgFileName),
				fmt.Sprintf("--service-cluster $(%s)", envoyNsEnvVar),
				fmt.Sprintf("--service-node $(%s)", envoyPodEnvVar),
				"--log-level info",
			},
			Env: []corev1.EnvVar{
				{
					Name: envoyNsEnvVar,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							APIVersion: "v1",
							FieldPath:  "metadata.namespace",
						},
					},
				},
				{
					Name: envoyPodEnvVar,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							APIVersion: "v1",
							FieldPath:  "metadata.name",
						},
					},
				},
			},
			ReadinessProbe: &corev1.Probe{
				FailureThreshold: int32(3),
				Handler: corev1.Handler{
					HTTPGet: &corev1.HTTPGetAction{
						Scheme: corev1.URISchemeHTTP,
						Path:   "/ready",
						Port:   intstr.IntOrString{IntVal: int32(8002)},
					},
				},
				InitialDelaySeconds: int32(3),
				PeriodSeconds:       int32(4),
				SuccessThreshold:    int32(1),
				TimeoutSeconds:      int32(1),
			},
			Ports: ports,
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      envoyCertsVolName,
					MountPath: filepath.Join("/", envoyCertsVolMntDir),
					ReadOnly:  true,
				},
				{
					Name:      envoyCfgVolName,
					MountPath: filepath.Join("/", envoyCfgVolMntDir),
					ReadOnly:  true,
				},
				{
					Name:      envoyAdminVolName,
					MountPath: filepath.Join("/", envoyAdminVolMntDir),
				},
			},
			Lifecycle: &corev1.Lifecycle{
				PreStop: &corev1.Handler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/shutdown",
						Port:   intstr.FromInt(8090),
						Scheme: "HTTP",
					},
				},
			},
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			TerminationMessagePath:   "/dev/termination-log",
		},
	}

	initContainers := []corev1.Container{
		{
			Name:            envoyInitContainerName,
			Image:           SesameImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{
				"sesame",
			},
			Args: []string{
				"bootstrap",
				filepath.Join("/", envoyCfgVolMntDir, envoyCfgFileName),
				"--xds-address=sesame",
				fmt.Sprintf("--xds-port=%d", objcfg.XDSPort),
				fmt.Sprintf("--xds-resource-version=%s", xdsResourceVersion),
				fmt.Sprintf("--resources-dir=%s", filepath.Join("/", envoyCfgVolMntDir, "resources")),
				fmt.Sprintf("--envoy-cafile=%s", filepath.Join("/", envoyCertsVolMntDir, "ca.crt")),
				fmt.Sprintf("--envoy-cert-file=%s", filepath.Join("/", envoyCertsVolMntDir, "tls.crt")),
				fmt.Sprintf("--envoy-key-file=%s", filepath.Join("/", envoyCertsVolMntDir, "tls.key")),
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      envoyCertsVolName,
					MountPath: filepath.Join("/", envoyCertsVolMntDir),
					ReadOnly:  true,
				},
				{
					Name:      envoyCfgVolName,
					MountPath: filepath.Join("/", envoyCfgVolMntDir),
					ReadOnly:  false,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name: envoyNsEnvVar,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							APIVersion: "v1",
							FieldPath:  "metadata.namespace",
						},
					},
				},
			},
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			TerminationMessagePath:   "/dev/termination-log",
		},
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: Sesame.Spec.Namespace.Name,
			Name:      envoyDaemonSetName,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			RevisionHistoryLimit: pointer.Int32Ptr(int32(10)),
			// Ensure the deamonset adopts only its own pods.
			Selector: EnvoyDaemonSetPodSelector(),
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: opintstr.PointerTo(intstr.FromString("10%")),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					// TODO [danehans]: Remove the prometheus annotations when Sesame is updated to
					// show how the Prometheus Operator is used to scrape Sesame/Envoy metrics.
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   "8002",
						"prometheus.io/path":   "/stats/prometheus",
					},
					Labels: EnvoyDaemonSetPodSelector().MatchLabels,
				},
				Spec: corev1.PodSpec{
					Containers:     containers,
					InitContainers: initContainers,
					Volumes: []corev1.Volume{
						{
							Name: envoyCertsVolName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									DefaultMode: pointer.Int32Ptr(int32(420)),
									SecretName:  envoyCertsSecretName,
								},
							},
						},
						{
							Name: envoyCfgVolName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: envoyAdminVolName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					ServiceAccountName:            objutil.EnvoyRbacName,
					DeprecatedServiceAccount:      EnvoyContainerName,
					AutomountServiceAccountToken:  pointer.BoolPtr(false),
					TerminationGracePeriodSeconds: pointer.Int64Ptr(int64(300)),
					SecurityContext:               objutil.NewUnprivilegedPodSecurity(),
					DNSPolicy:                     corev1.DNSClusterFirst,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 "default-scheduler",
				},
			},
		},
	}

	if Sesame.EnvoyNodeSelectorExists() {
		ds.Spec.Template.Spec.NodeSelector = Sesame.Spec.NodePlacement.Envoy.NodeSelector
	}

	if Sesame.EnvoyTolerationsExist() {
		ds.Spec.Template.Spec.Tolerations = Sesame.Spec.NodePlacement.Envoy.Tolerations
	}

	return ds
}

// CurrentDaemonSet returns the current DaemonSet resource for the provided sesame.
func CurrentDaemonSet(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) (*appsv1.DaemonSet, error) {
	ds := &appsv1.DaemonSet{}
	key := types.NamespacedName{
		Namespace: Sesame.Spec.Namespace.Name,
		Name:      envoyDaemonSetName,
	}
	if err := cli.Get(ctx, key, ds); err != nil {
		return nil, err
	}
	return ds, nil
}

// createDaemonSet creates a DaemonSet resource for the provided ds.
func createDaemonSet(ctx context.Context, cli client.Client, ds *appsv1.DaemonSet) error {
	if err := cli.Create(ctx, ds); err != nil {
		return fmt.Errorf("failed to create daemonset %s/%s: %w", ds.Namespace, ds.Name, err)
	}
	return nil
}

// updateDaemonSetIfNeeded updates a DaemonSet if current does not match desired,
// using sesame to verify the existence of owner labels.
func updateDaemonSetIfNeeded(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, current, desired *appsv1.DaemonSet) error {
	if labels.Exist(current, objSesame.OwnerLabels(Sesame)) {
		ds, updated := equality.DaemonsetConfigChanged(current, desired)
		if updated {
			if err := cli.Update(ctx, ds); err != nil {
				return fmt.Errorf("failed to update daemonset %s/%s: %w", ds.Namespace, ds.Name, err)
			}
			return nil
		}
	}
	return nil
}

// EnvoyDaemonSetPodSelector returns a label selector using "app: envoy" as the
// key/value pair.
//
// TODO [danehans]: Update to use "sesame.operator.projectsesame.io/daemonset-envoy"
// when https://github.com/projectsesame/Sesame/issues/1821 is fixed.
func EnvoyDaemonSetPodSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app": "envoy",
		},
	}
}
