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

package job

import (
	"context"
	"fmt"
	"time"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	"github.com/projectsesame/sesame-operator/internal/config"
	"github.com/projectsesame/sesame-operator/internal/equality"
	objutil "github.com/projectsesame/sesame-operator/internal/objects"
	objSesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	labels "github.com/projectsesame/sesame-operator/pkg/labels"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	jobContainerName = "sesame"
	jobNsEnvVar      = "Sesame_NAMESPACE"
)

var (
	// certgenJobName is the name of Certgen's Job resource.
	// [TODO] danehans: Remove and use sesame.Name + "-certgen" when
	// https://github.com/projectsesame/Sesame/issues/2122 is fixed.
	certgenJobName = "sesame-certgen-" + objutil.TagFromImage(config.DefaultSesameImage)
)

// EnsureJob ensures that a Job exists for the given sesame.
// TODO [danehans]: The real dependency is whether the TLS secrets are present.
// The method should first check for the secrets, then use certgen as a secret
// generating strategy.
func EnsureJob(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, image string) error {
	desired := DesiredJob(Sesame, image)
	current, err := currentJob(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return createJob(ctx, cli, desired)
		}
		return fmt.Errorf("failed to get job %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if err := recreateJobIfNeeded(ctx, cli, Sesame, current, desired); err != nil {
		return fmt.Errorf("failed to recreate job %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	return nil
}

// EnsureJobDeleted ensures the Job for the provided sesame is deleted if
// Sesame owner labels exist.
func EnsureJobDeleted(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) error {
	job, err := currentJob(ctx, cli, Sesame)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if labels.Exist(job, objSesame.OwnerLabels(Sesame)) {
		if err := cli.Delete(ctx, job); err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
			return nil
		}
	}
	return nil
}

// currentJob returns the current Job resource named name for the provided sesame.
func currentJob(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame) (*batchv1.Job, error) {
	current := &batchv1.Job{}
	key := types.NamespacedName{
		Namespace: Sesame.Spec.Namespace.Name,
		Name:      certgenJobName,
	}
	err := cli.Get(ctx, key, current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

// DesiredJob generates the desired Job resource using image for the given sesame.
func DesiredJob(Sesame *operatorv1alpha1.Sesame, image string) *batchv1.Job {
	env := corev1.EnvVar{
		Name: jobNsEnvVar,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				APIVersion: "v1",
				FieldPath:  "metadata.namespace",
			},
		},
	}
	container := corev1.Container{
		Name:            jobContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			"sesame",
			"certgen",
			"--kube",
			"--incluster",
			"--overwrite",
			"--secrets-format=compact",
			fmt.Sprintf("--namespace=$(%s)", jobNsEnvVar),
		},
		Env:                      []corev1.EnvVar{env},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: "File",
	}
	spec := corev1.PodSpec{
		Containers:                    []corev1.Container{container},
		DeprecatedServiceAccount:      objutil.CertGenRbacName,
		ServiceAccountName:            objutil.CertGenRbacName,
		SecurityContext:               objutil.NewUnprivilegedPodSecurity(),
		RestartPolicy:                 corev1.RestartPolicyNever,
		DNSPolicy:                     corev1.DNSClusterFirst,
		SchedulerName:                 "default-scheduler",
		TerminationGracePeriodSeconds: pointer.Int64Ptr(int64(30)),
	}
	// TODO [danehans] certgen needs to be updated to match these labels.
	// See https://github.com/projectsesame/Sesame/issues/1821 for details.
	labels := map[string]string{
		"app.kubernetes.io/name":       "sesame-certgen",
		"app.kubernetes.io/instance":   Sesame.Name,
		"app.kubernetes.io/component":  "ingress-controller",
		"app.kubernetes.io/part-of":    "project-sesame",
		"app.kubernetes.io/managed-by": "sesame-operator",
		// associate the job with the provided sesame.
		operatorv1alpha1.OwningSesameNameLabel: Sesame.Name,
		operatorv1alpha1.OwningSesameNsLabel:   Sesame.Namespace,
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      certgenJobName,
			Namespace: Sesame.Spec.Namespace.Name,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Parallelism:  pointer.Int32Ptr(int32(1)),
			Completions:  pointer.Int32Ptr(int32(1)),
			BackoffLimit: pointer.Int32Ptr(int32(1)),
			// Make job eligible to for immediate deletion (feature gate dependent).
			TTLSecondsAfterFinished: pointer.Int32Ptr(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: objSesame.OwningSelector(Sesame).MatchLabels,
				},
				Spec: spec,
			},
		},
	}
	return job
}

// recreateJobIfNeeded recreates a Job if current doesn't match desired,
// using sesame to verify the existence of owner labels.
func recreateJobIfNeeded(ctx context.Context, cli client.Client, Sesame *operatorv1alpha1.Sesame, current, desired *batchv1.Job) error {
	if labels.Exist(current, objSesame.OwnerLabels(Sesame)) {
		updated, changed := equality.JobConfigChanged(current, desired)
		if !changed {
			return nil
		}
		if err := cli.Delete(ctx, updated); err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}
		// Retry is needed since the object may still be getting deleted.
		if err := retryJobCreate(ctx, cli, updated, time.Second*3); err != nil {
			return err
		}
	}
	return nil
}

// createJob creates a Job resource for the provided job.
func createJob(ctx context.Context, cli client.Client, job *batchv1.Job) error {
	if err := cli.Create(ctx, job); err != nil {
		return fmt.Errorf("failed to create job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}

// retryJobCreate tries creating the provided Job, retrying every second
// until timeout is reached.
func retryJobCreate(ctx context.Context, cli client.Client, job *batchv1.Job, timeout time.Duration) error {
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := cli.Create(ctx, job); err != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to create job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}
