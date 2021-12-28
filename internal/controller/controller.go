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

package controller

import (
	"context"
	"fmt"

	operatorv1alpha1 "github.com/projectsesame/sesame-operator/api/v1alpha1"
	objutil "github.com/projectsesame/sesame-operator/internal/objects"
	objcm "github.com/projectsesame/sesame-operator/internal/objects/configmap"
	objds "github.com/projectsesame/sesame-operator/internal/objects/daemonset"
	objdeploy "github.com/projectsesame/sesame-operator/internal/objects/deployment"
	objjob "github.com/projectsesame/sesame-operator/internal/objects/job"
	objns "github.com/projectsesame/sesame-operator/internal/objects/namespace"
	objsvc "github.com/projectsesame/sesame-operator/internal/objects/service"
	objsesame "github.com/projectsesame/sesame-operator/internal/objects/sesame"
	retryable "github.com/projectsesame/sesame-operator/internal/retryableerror"
	"github.com/projectsesame/sesame-operator/internal/status"
	"github.com/projectsesame/sesame-operator/pkg/validation"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "sesame_controller"
)

// Config holds all the things necessary for the controller to run.
type Config struct {
	// SesameImage is the name of the Sesame container image.
	SesameImage string
	// EnvoyImage is the name of the Envoy container image.
	EnvoyImage string
}

// reconciler reconciles a Sesame object.
type reconciler struct {
	config Config
	client client.Client
	log    logr.Logger
}

// New creates the sesame controller from mgr and cfg. The controller will be pre-configured
// to watch for Sesame objects across all namespaces.
func New(mgr manager.Manager, cfg Config) (controller.Controller, error) {
	r := &reconciler{
		config: cfg,
		client: mgr.GetClient(),
		log:    ctrl.Log.WithName(controllerName),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &operatorv1alpha1.Sesame{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	// Watch the Sesame deployment and Envoy daemonset to properly surface Sesame status conditions.
	if err := c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, r.enqueueRequestForOwningSesame()); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, r.enqueueRequestForOwningSesame()); err != nil {
		return nil, err
	}
	return c, nil
}

// enqueueRequestForOwningSesame returns an event handler that maps events to
// objects containing Sesame owner labels.
func (r *reconciler) enqueueRequestForOwningSesame() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(a client.Object) []reconcile.Request {
		labels := a.GetLabels()
		ns, nsFound := labels[operatorv1alpha1.OwningSesameNsLabel]
		name, nameFound := labels[operatorv1alpha1.OwningSesameNameLabel]
		if nsFound && nameFound {
			r.log.Info("queueing sesame", "namespace", ns, "name", name, "related", a.GetSelfLink())
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: ns,
						Name:      name,
					},
				},
			}
		}
		return []reconcile.Request{}
	})
}

// Reconcile reconciles watched objects and attempts to make the current state of
// the object match the desired state.
func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.log.WithValues("sesame", req.NamespacedName)
	r.log.Info("reconciling", "request", req)
	// Only proceed if we can get the state of sesame.
	Sesame := &operatorv1alpha1.Sesame{}
	if err := r.client.Get(ctx, req.NamespacedName, Sesame); err != nil {
		if errors.IsNotFound(err) {
			// This means the sesame was already deleted/finalized and there are
			// stale queue entries (or something edge triggering from a related
			// resource that got deleted async).
			r.log.Info("sesame not found; reconciliation will be skipped", "request", req)
			return ctrl.Result{}, nil
		}
		// Error reading the object, so requeue the request.
		return ctrl.Result{}, fmt.Errorf("failed to get sesame %s: %w", req, err)
	}
	// The sesame is safe to process, so ensure current state matches desired state.
	desired := Sesame.ObjectMeta.DeletionTimestamp.IsZero()
	if desired {
		if err := validation.Sesame(ctx, r.client, Sesame); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to validate sesame %s/%s: %w", Sesame.Namespace, Sesame.Name, err)
		}
		if !Sesame.IsFinalized() {
			// Before doing anything with the sesame, ensure it has a finalizer
			// so it can cleaned-up later.
			if err := objsesame.EnsureFinalizer(ctx, r.client, Sesame); err != nil {
				return ctrl.Result{}, err
			}
			r.log.Info("finalized sesame", "namespace", Sesame.Namespace, "name", Sesame.Name)
		} else {
			r.log.Info("sesame finalized", "namespace", Sesame.Namespace, "name", Sesame.Name)
			if err := r.ensureSesame(ctx, Sesame); err != nil {
				switch e := err.(type) {
				case retryable.Error:
					r.log.Error(e, "got retryable error; requeueing", "after", e.After())
					return ctrl.Result{RequeueAfter: e.After()}, nil
				default:
					return ctrl.Result{}, err
				}
			}
			r.log.Info("ensured sesame", "namespace", Sesame.Namespace, "name", Sesame.Name)
		}
	} else {
		if err := r.ensureSesameDeleted(ctx, Sesame); err != nil {
			switch e := err.(type) {
			case retryable.Error:
				r.log.Error(e, "got retryable error; requeueing", "after", e.After())
				return ctrl.Result{RequeueAfter: e.After()}, nil
			default:
				return ctrl.Result{}, err
			}
		}
		r.log.Info("deleted sesame", "namespace", Sesame.Namespace, "name", Sesame.Name)
	}
	return ctrl.Result{}, nil
}

// ensureSesame ensures all necessary resources exist for the given sesame.
func (r *reconciler) ensureSesame(ctx context.Context, Sesame *operatorv1alpha1.Sesame) error {
	var errs []error
	cli := r.client

	handleResult := func(resource string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to ensure %s for sesame %s/%s: %w", resource, Sesame.Namespace, Sesame.Name, err))
		} else {
			r.log.Info(fmt.Sprintf("ensured %s for sesame", resource), "namespace", Sesame.Namespace, "name", Sesame.Name)
		}
	}

	syncSesameStatus := func() error {
		if err := status.SyncSesame(ctx, cli, Sesame); err != nil {
			errs = append(errs, fmt.Errorf("failed to sync status for sesame %s/%s: %w", Sesame.Namespace, Sesame.Name, err))
		} else {
			r.log.Info("synced status for sesame", "namespace", Sesame.Namespace, "name", Sesame.Name)
		}
		return retryable.NewMaybeRetryableAggregate(errs)
	}

	handleResult("namespace", objns.EnsureNamespace(ctx, cli, Sesame))
	handleResult("rbac", objutil.EnsureRBAC(ctx, cli, Sesame))

	if len(errs) > 0 {
		return syncSesameStatus()
	}

	SesameImage := r.config.SesameImage
	envoyImage := r.config.EnvoyImage

	handleResult("configmap", objcm.EnsureConfigMap(ctx, cli, Sesame))
	handleResult("job", objjob.EnsureJob(ctx, cli, Sesame, SesameImage))
	handleResult("deployment", objdeploy.EnsureDeployment(ctx, cli, Sesame, SesameImage))
	handleResult("daemonset", objds.EnsureDaemonSet(ctx, cli, Sesame, SesameImage, envoyImage))
	handleResult("sesame service", objsvc.EnsureSesameService(ctx, cli, Sesame))

	switch Sesame.Spec.NetworkPublishing.Envoy.Type {
	case operatorv1alpha1.LoadBalancerServicePublishingType, operatorv1alpha1.NodePortServicePublishingType, operatorv1alpha1.ClusterIPServicePublishingType:
		handleResult("envoy service", objsvc.EnsureEnvoyService(ctx, cli, Sesame))
	}

	return syncSesameStatus()
}

// ensureSesameDeleted ensures sesame and all child resources have been deleted.
func (r *reconciler) ensureSesameDeleted(ctx context.Context, Sesame *operatorv1alpha1.Sesame) error {
	var errs []error
	cli := r.client

	handleResult := func(resource string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to delete %s for sesame %s/%s: %w", resource, Sesame.Namespace, Sesame.Name, err))
		} else {
			r.log.Info(fmt.Sprintf("deleted %s for sesame", resource), "namespace", Sesame.Namespace, "name", Sesame.Name)
		}
	}

	switch Sesame.Spec.NetworkPublishing.Envoy.Type {
	case operatorv1alpha1.LoadBalancerServicePublishingType, operatorv1alpha1.NodePortServicePublishingType, operatorv1alpha1.ClusterIPServicePublishingType:
		handleResult("envoy service", objsvc.EnsureEnvoyServiceDeleted(ctx, cli, Sesame))
	}

	handleResult("service", objsvc.EnsureSesameServiceDeleted(ctx, cli, Sesame))
	handleResult("daemonset", objds.EnsureDaemonSetDeleted(ctx, cli, Sesame))
	handleResult("deployment", objdeploy.EnsureDeploymentDeleted(ctx, cli, Sesame))
	handleResult("job", objjob.EnsureJobDeleted(ctx, cli, Sesame))
	handleResult("configmap", objcm.EnsureConfigMapDeleted(ctx, cli, Sesame))
	handleResult("rbac", objutil.EnsureRBACDeleted(ctx, cli, Sesame))
	if deleteExpected, err := objns.EnsureNamespaceDeleted(ctx, cli, Sesame); deleteExpected {
		handleResult("namespace", err)
	} else {
		r.log.Info("bypassing namespace deletion", "namespace", Sesame.Namespace, "name", Sesame.Name)
	}

	if len(errs) == 0 {
		if err := objsesame.EnsureFinalizerRemoved(ctx, cli, Sesame); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove finalizer from sesame %s/%s: %w", Sesame.Namespace, Sesame.Name, err))
		} else {
			r.log.Info("removed finalizer from sesame", "namespace", Sesame.Namespace, "name", Sesame.Name)
		}
	}

	return utilerrors.NewAggregate(errs)
}
