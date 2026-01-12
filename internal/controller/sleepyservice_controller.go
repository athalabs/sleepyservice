// Copyright 2026 AthaLabs
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sleepyv1alpha1 "github.com/athalabs/sleepyservice/api/v1alpha1"
)

const (
	finalizerName = "sleepy.atha.gr/finalizer"
)

// SleepyServiceReconciler reconciles a SleepyService object
type SleepyServiceReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	OperatorImage string
}

// +kubebuilder:rbac:groups=sleepy.atha.gr,resources=sleepyservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sleepy.atha.gr,resources=sleepyservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sleepy.atha.gr,resources=sleepyservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/scale,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets/scale,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=clusters,verbs=get;list;watch;update;patch

func (r *SleepyServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the SleepyService
	var hs sleepyv1alpha1.SleepyService
	if err := r.Get(ctx, req.NamespacedName, &hs); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !hs.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &hs)
	}

	// Add finalizer if needed
	if !controllerutil.ContainsFinalizer(&hs, finalizerName) {
		controllerutil.AddFinalizer(&hs, finalizerName)
		if err := r.Update(ctx, &hs); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ensure proxy RBAC resources exist
	if err := r.ensureProxyServiceAccount(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure proxy service account")
		return ctrl.Result{}, err
	}

	if err := r.ensureProxyRole(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure proxy role")
		return ctrl.Result{}, err
	}

	if err := r.ensureProxyRoleBinding(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure proxy role binding")
		return ctrl.Result{}, err
	}

	// Ensure backend Service exists (if using new API)
	if err := r.ensureBackendService(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure backend service")
		return ctrl.Result{}, err
	}

	// Ensure proxy deployment exists
	if err := r.ensureProxyDeployment(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure proxy deployment")
		return ctrl.Result{}, err
	}

	// Ensure proxy service exists
	if err := r.ensureProxyService(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure proxy service")
		return ctrl.Result{}, err
	}

	// Update component statuses
	if err := r.updateComponentStatuses(ctx, &hs); err != nil {
		log.Error(err, "Failed to update component statuses")
		return ctrl.Result{}, err
	}

	// Update status with backend Service name (if using new API)
	if hs.Spec.BackendService != nil {
		backendSvcName := fmt.Sprintf("%s-actual", hs.Name)
		hs.Status.BackendService = backendSvcName
	}

	// Reconcile state based on DesiredState (status subresource pattern)
	if err := r.reconcileState(ctx, &hs); err != nil {
		log.Error(err, "Failed to reconcile state")
		return ctrl.Result{}, err
	}

	// Check for idle timeout
	if hs.Spec.IdleTimeout.Duration > 0 && hs.Status.State == sleepyv1alpha1.StateAwake {
		if hs.Status.LastActivity != nil {
			idleDuration := time.Since(hs.Status.LastActivity.Time)
			if idleDuration > hs.Spec.IdleTimeout.Duration {
				log.Info("Idle timeout reached, hibernating")
				return r.reconcileHibernate(ctx, &hs)
			}
			// Requeue to check idle timeout again
			requeueAfter := hs.Spec.IdleTimeout.Duration - idleDuration
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	// Update status
	if err := r.Status().Update(ctx, &hs); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *SleepyServiceReconciler) reconcileDelete(ctx context.Context, hs *sleepyv1alpha1.SleepyService) (ctrl.Result, error) {
	// Cleanup owned resources (proxy deployment/service will be garbage collected)

	// Remove finalizer
	controllerutil.RemoveFinalizer(hs, finalizerName)
	if err := r.Update(ctx, hs); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *SleepyServiceReconciler) reconcileState(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	log := log.FromContext(ctx)

	desiredState := hs.Status.DesiredState
	currentState := hs.Status.State

	// No desired state set, default to Sleeping
	if desiredState == "" {
		desiredState = sleepyv1alpha1.StateSleeping
	}

	// No current state set, initialize to Sleeping
	if currentState == "" {
		hs.Status.State = sleepyv1alpha1.StateSleeping
		currentState = sleepyv1alpha1.StateSleeping
	}

	// Handle state transitions
	switch {
	case desiredState == sleepyv1alpha1.StateAwake && currentState != sleepyv1alpha1.StateAwake && currentState != sleepyv1alpha1.StateWaking:
		// Proxy wants awake, but we're sleeping - start wake-up
		log.Info("Starting wake-up sequence", "desiredState", desiredState, "currentState", currentState)
		if err := r.scaleUpComponents(ctx, hs); err != nil {
			return err
		}
		hs.Status.State = sleepyv1alpha1.StateWaking
		now := metav1.Now()
		hs.Status.LastTransition = &now

	case currentState == sleepyv1alpha1.StateWaking:
		// Currently waking - check if all components are ready
		allReady := true
		for _, comp := range hs.Status.Components {
			if !comp.Ready {
				allReady = false
				break
			}
		}

		if allReady {
			log.Info("All components ready, transitioning to Awake")
			hs.Status.State = sleepyv1alpha1.StateAwake
			now := metav1.Now()
			hs.Status.LastTransition = &now
		}

	case desiredState == sleepyv1alpha1.StateSleeping && currentState == sleepyv1alpha1.StateAwake:
		// Proxy wants sleep (idle timeout), and we're awake - hibernate
		log.Info("Starting hibernation", "desiredState", desiredState, "currentState", currentState)
		if err := r.scaleDownComponents(ctx, hs); err != nil {
			return err
		}
		hs.Status.State = sleepyv1alpha1.StateSleeping
		now := metav1.Now()
		hs.Status.LastTransition = &now
	}

	return nil
}

func (r *SleepyServiceReconciler) scaleUpComponents(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	log := log.FromContext(ctx)

	for _, comp := range hs.Spec.Components {
		ns := comp.Ref.Namespace
		if ns == "" {
			ns = hs.Namespace
		}

		switch comp.Type {
		case sleepyv1alpha1.ComponentTypeDeployment:
			replicas := int32(1)
			if comp.Replicas != nil {
				replicas = *comp.Replicas
			}

			var deploy appsv1.Deployment
			if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: ns}, &deploy); err != nil {
				return fmt.Errorf("failed to get Deployment %s: %w", comp.Ref.Name, err)
			}

			if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas == 0 {
				log.Info("Scaling up Deployment", "name", comp.Ref.Name, "replicas", replicas)
				deploy.Spec.Replicas = &replicas
				if err := r.Update(ctx, &deploy); err != nil {
					return fmt.Errorf("failed to scale up Deployment %s: %w", comp.Ref.Name, err)
				}
			}

		case sleepyv1alpha1.ComponentTypeStatefulSet:
			replicas := int32(1)
			if comp.Replicas != nil {
				replicas = *comp.Replicas
			}

			var sts appsv1.StatefulSet
			if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: ns}, &sts); err != nil {
				return fmt.Errorf("failed to get StatefulSet %s: %w", comp.Ref.Name, err)
			}

			if sts.Spec.Replicas == nil || *sts.Spec.Replicas == 0 {
				log.Info("Scaling up StatefulSet", "name", comp.Ref.Name, "replicas", replicas)
				sts.Spec.Replicas = &replicas
				if err := r.Update(ctx, &sts); err != nil {
					return fmt.Errorf("failed to scale up StatefulSet %s: %w", comp.Ref.Name, err)
				}
			}

		case sleepyv1alpha1.ComponentTypeCNPGCluster:
			// Wake CNPG cluster by removing hibernation annotation
			cluster := &unstructured.Unstructured{}
			cluster.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "postgresql.cnpg.io",
				Version: "v1",
				Kind:    "Cluster",
			})

			if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: ns}, cluster); err != nil {
				return fmt.Errorf("failed to get CNPG Cluster %s: %w", comp.Ref.Name, err)
			}

			annotations := cluster.GetAnnotations()
			if annotations != nil && annotations["cnpg.io/hibernation"] == "on" {
				log.Info("Waking CNPG Cluster", "name", comp.Ref.Name)
				delete(annotations, "cnpg.io/hibernation")
				cluster.SetAnnotations(annotations)
				if err := r.Update(ctx, cluster); err != nil {
					return fmt.Errorf("failed to wake CNPG Cluster %s: %w", comp.Ref.Name, err)
				}
			}
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) scaleDownComponents(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	log := log.FromContext(ctx)

	for _, comp := range hs.Spec.Components {
		ns := comp.Ref.Namespace
		if ns == "" {
			ns = hs.Namespace
		}

		switch comp.Type {
		case sleepyv1alpha1.ComponentTypeDeployment:
			var deploy appsv1.Deployment
			if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: ns}, &deploy); err != nil {
				return fmt.Errorf("failed to get Deployment %s: %w", comp.Ref.Name, err)
			}

			if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
				log.Info("Scaling down Deployment", "name", comp.Ref.Name)
				replicas := int32(0)
				deploy.Spec.Replicas = &replicas
				if err := r.Update(ctx, &deploy); err != nil {
					return fmt.Errorf("failed to scale down Deployment %s: %w", comp.Ref.Name, err)
				}
			}

		case sleepyv1alpha1.ComponentTypeStatefulSet:
			var sts appsv1.StatefulSet
			if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: ns}, &sts); err != nil {
				return fmt.Errorf("failed to get StatefulSet %s: %w", comp.Ref.Name, err)
			}

			if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
				log.Info("Scaling down StatefulSet", "name", comp.Ref.Name)
				replicas := int32(0)
				sts.Spec.Replicas = &replicas
				if err := r.Update(ctx, &sts); err != nil {
					return fmt.Errorf("failed to scale down StatefulSet %s: %w", comp.Ref.Name, err)
				}
			}

		case sleepyv1alpha1.ComponentTypeCNPGCluster:
			// Hibernate CNPG cluster by setting hibernation annotation
			cluster := &unstructured.Unstructured{}
			cluster.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "postgresql.cnpg.io",
				Version: "v1",
				Kind:    "Cluster",
			})

			if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: ns}, cluster); err != nil {
				return fmt.Errorf("failed to get CNPG Cluster %s: %w", comp.Ref.Name, err)
			}

			annotations := cluster.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}

			if annotations["cnpg.io/hibernation"] != "on" {
				log.Info("Hibernating CNPG Cluster", "name", comp.Ref.Name)
				annotations["cnpg.io/hibernation"] = "on"
				cluster.SetAnnotations(annotations)
				if err := r.Update(ctx, cluster); err != nil {
					return fmt.Errorf("failed to hibernate CNPG Cluster %s: %w", comp.Ref.Name, err)
				}
			}
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) ensureProxyDeployment(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	proxyName := hs.Name
	deploymentName := fmt.Sprintf("%s-wakeproxy", hs.Name)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: hs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		// Set owner reference for garbage collection
		if err := controllerutil.SetControllerReference(hs, deploy, r.Scheme); err != nil {
			return err
		}

		labels := map[string]string{
			"app.kubernetes.io/name":       "wake-proxy",
			"app.kubernetes.io/instance":   hs.Name,
			"app.kubernetes.io/managed-by": "hibernating-service-controller",
		}

		replicas := int32(1)
		deploy.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: proxyName,
					Containers: []corev1.Container{
						{
							Name:    "proxy",
							Image:   r.OperatorImage,
							Command: []string{"/proxy"},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 8080,
								},
							},
							Env: r.buildProxyEnv(hs),
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("16Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		}

		return nil
	})

	if err != nil {
		return err
	}

	hs.Status.ProxyDeployment = deploymentName
	return nil
}

func (r *SleepyServiceReconciler) ensureProxyServiceAccount(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	proxyName := hs.Name

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      proxyName,
			Namespace: hs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		return controllerutil.SetControllerReference(hs, sa, r.Scheme)
	})

	return err
}

func (r *SleepyServiceReconciler) ensureProxyRole(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	proxyName := hs.Name

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      proxyName,
			Namespace: hs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if err := controllerutil.SetControllerReference(hs, role, r.Scheme); err != nil {
			return err
		}

		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"sleepy.atha.gr"},
				Resources: []string{"sleepyservices"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"sleepy.atha.gr"},
				Resources: []string{"sleepyservices/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		}

		return nil
	})

	return err
}

func (r *SleepyServiceReconciler) ensureProxyRoleBinding(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	proxyName := hs.Name

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      proxyName,
			Namespace: hs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		if err := controllerutil.SetControllerReference(hs, rb, r.Scheme); err != nil {
			return err
		}

		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     proxyName,
		}

		rb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      proxyName,
				Namespace: hs.Namespace,
			},
		}

		return nil
	})

	return err
}

func (r *SleepyServiceReconciler) buildProxyEnv(hs *sleepyv1alpha1.SleepyService) []corev1.EnvVar {
	// Find the last application component (Deployment or StatefulSet)
	// Last one is typically the main app, after dependencies like databases
	var appComponent *sleepyv1alpha1.Component
	var cnpgComponent *sleepyv1alpha1.Component

	for i := range hs.Spec.Components {
		c := &hs.Spec.Components[i]
		switch c.Type {
		case sleepyv1alpha1.ComponentTypeCNPGCluster:
			cnpgComponent = c
		case sleepyv1alpha1.ComponentTypeDeployment, sleepyv1alpha1.ComponentTypeStatefulSet:
			appComponent = c // Last one wins
		}
	}

	env := []corev1.EnvVar{
		{Name: "NAMESPACE", Value: hs.Namespace},
		{Name: "HIBERNATING_SERVICE_NAME", Value: hs.Name},
		{Name: "HEALTH_PATH", Value: r.getHealthPath(hs)},
		{Name: "WAKE_TIMEOUT", Value: hs.Spec.WakeTimeout.Duration.String()},
		{Name: "IDLE_TIMEOUT", Value: hs.Spec.IdleTimeout.Duration.String()},
	}

	if appComponent != nil {
		ns := appComponent.Ref.Namespace
		if ns == "" {
			ns = hs.Namespace
		}

		// Construct backend URL
		var backendURL string
		if hs.Spec.BackendService != nil {
			// New API: use managed backend service
			backendSvcName := fmt.Sprintf("%s-actual", hs.Name)
			port := r.getBackendServicePort(hs)
			backendURL = fmt.Sprintf("http://%s.%s:%d", backendSvcName, hs.Namespace, port)
		} else {
			// Old API: use deployment name as service name
			port := r.getBackendServicePort(hs)
			backendURL = fmt.Sprintf("http://%s.%s:%d", appComponent.Ref.Name, ns, port)
		}

		env = append(env,
			corev1.EnvVar{Name: "DEPLOYMENT_NAME", Value: appComponent.Ref.Name},
			corev1.EnvVar{Name: "BACKEND_URL", Value: backendURL},
		)
		if appComponent.Replicas != nil {
			env = append(env, corev1.EnvVar{Name: "DESIRED_REPLICAS", Value: fmt.Sprintf("%d", *appComponent.Replicas)})
		}
	}

	if cnpgComponent != nil {
		env = append(env, corev1.EnvVar{Name: "CNPG_CLUSTER_NAME", Value: cnpgComponent.Ref.Name})
	}

	return env
}

func (r *SleepyServiceReconciler) ensureProxyService(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	svcName := hs.Name

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: hs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(hs, svc, r.Scheme); err != nil {
			return err
		}

		svc.Spec = corev1.ServiceSpec{
			Selector: map[string]string{
				"app.kubernetes.io/name":     "wake-proxy",
				"app.kubernetes.io/instance": hs.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		}

		return nil
	})

	return err
}

func (r *SleepyServiceReconciler) ensureBackendService(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	log := log.FromContext(ctx)

	// Check if backend Service creation is disabled
	if hs.Spec.BackendService != nil && hs.Spec.BackendService.Enabled != nil && !*hs.Spec.BackendService.Enabled {
		log.Info("Backend Service creation disabled")
		return nil
	}

	// Find the last application component (Deployment or StatefulSet)
	// Last one is typically the main app, after dependencies like databases
	var appComponent *sleepyv1alpha1.Component
	for i := range hs.Spec.Components {
		if hs.Spec.Components[i].Type == sleepyv1alpha1.ComponentTypeDeployment ||
			hs.Spec.Components[i].Type == sleepyv1alpha1.ComponentTypeStatefulSet {
			appComponent = &hs.Spec.Components[i]
			// Don't break - keep looping to get the last one
		}
	}

	if appComponent == nil {
		return fmt.Errorf("no Deployment or StatefulSet component found")
	}

	// Get the Deployment/StatefulSet to extract pod selectors
	ns := appComponent.Ref.Namespace
	if ns == "" {
		ns = hs.Namespace
	}

	var podSelector map[string]string

	if appComponent.Type == sleepyv1alpha1.ComponentTypeDeployment {
		var deploy appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: appComponent.Ref.Name, Namespace: ns}, &deploy); err != nil {
			return fmt.Errorf("failed to get Deployment: %w", err)
		}
		podSelector = deploy.Spec.Selector.MatchLabels
	} else {
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: appComponent.Ref.Name, Namespace: ns}, &sts); err != nil {
			return fmt.Errorf("failed to get StatefulSet: %w", err)
		}
		podSelector = sts.Spec.Selector.MatchLabels
	}

	if len(podSelector) == 0 {
		return fmt.Errorf("no pod selector labels found on component")
	}

	// Create the backend Service
	backendSvcName := fmt.Sprintf("%s-actual", hs.Name)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backendSvcName,
			Namespace: hs.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		// Set owner reference for garbage collection
		if err := controllerutil.SetControllerReference(hs, svc, r.Scheme); err != nil {
			return err
		}

		// Set annotations if provided
		if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Annotations) > 0 {
			if svc.Annotations == nil {
				svc.Annotations = make(map[string]string)
			}
			for k, v := range hs.Spec.BackendService.Annotations {
				svc.Annotations[k] = v
			}
		}

		// Configure Service spec
		svc.Spec.Selector = podSelector

		// Set service type
		svcType := corev1.ServiceTypeClusterIP
		if hs.Spec.BackendService != nil && hs.Spec.BackendService.Type != "" {
			svcType = hs.Spec.BackendService.Type
		}
		svc.Spec.Type = svcType

		// Configure ports
		ports := r.buildBackendServicePorts(hs)
		svc.Spec.Ports = ports

		// Optional fields
		if hs.Spec.BackendService != nil {
			if hs.Spec.BackendService.ClusterIP != "" {
				svc.Spec.ClusterIP = hs.Spec.BackendService.ClusterIP
			}
			if len(hs.Spec.BackendService.ExternalIPs) > 0 {
				svc.Spec.ExternalIPs = hs.Spec.BackendService.ExternalIPs
			}
			if hs.Spec.BackendService.LoadBalancerIP != "" {
				svc.Spec.LoadBalancerIP = hs.Spec.BackendService.LoadBalancerIP
			}
			if hs.Spec.BackendService.SessionAffinity != "" {
				svc.Spec.SessionAffinity = hs.Spec.BackendService.SessionAffinity
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create/update backend Service: %w", err)
	}

	log.Info("Backend Service ensured", "name", backendSvcName)
	return nil
}

func (r *SleepyServiceReconciler) buildBackendServicePorts(hs *sleepyv1alpha1.SleepyService) []corev1.ServicePort {
	// If user specified ports explicitly, use those
	if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Ports) > 0 {
		ports := make([]corev1.ServicePort, len(hs.Spec.BackendService.Ports))
		for i, p := range hs.Spec.BackendService.Ports {
			targetPort := p.TargetPort
			if targetPort.IntVal == 0 && targetPort.StrVal == "" {
				targetPort = intstr.FromInt32(p.Port)
			}

			protocol := corev1.ProtocolTCP
			if p.Protocol != "" {
				protocol = p.Protocol
			}

			ports[i] = corev1.ServicePort{
				Name:       p.Name,
				Protocol:   protocol,
				Port:       p.Port,
				TargetPort: targetPort,
				NodePort:   p.NodePort,
			}
		}
		return ports
	}

	// Default to port 80 if nothing specified
	return []corev1.ServicePort{
		{
			Name:       "http",
			Protocol:   corev1.ProtocolTCP,
			Port:       80,
			TargetPort: intstr.FromInt32(80),
		},
	}
}

func (r *SleepyServiceReconciler) getBackendServicePort(hs *sleepyv1alpha1.SleepyService) int32 {
	// New API: use BackendService.Ports
	if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Ports) > 0 {
		return hs.Spec.BackendService.Ports[0].Port
	}
	// Default
	return 80
}

func (r *SleepyServiceReconciler) getHealthPath(hs *sleepyv1alpha1.SleepyService) string {
	// New API: use top-level HealthPath
	if hs.Spec.HealthPath != "" {
		return hs.Spec.HealthPath
	}
	// Default
	return "/health"
}

//nolint:unparam
func (r *SleepyServiceReconciler) updateComponentStatuses(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	statuses := make([]sleepyv1alpha1.ComponentStatus, 0, len(hs.Spec.Components))
	allReady := true

	for _, comp := range hs.Spec.Components {
		status := sleepyv1alpha1.ComponentStatus{Name: comp.Name}

		ns := comp.Ref.Namespace
		if ns == "" {
			ns = hs.Namespace
		}

		switch comp.Type {
		case sleepyv1alpha1.ComponentTypeDeployment:
			ready, msg, err := r.checkDeploymentReady(ctx, ns, comp.Ref.Name)
			if err != nil {
				status.Message = err.Error()
			} else {
				status.Ready = ready
				status.Message = msg
			}

		case sleepyv1alpha1.ComponentTypeStatefulSet:
			ready, msg, err := r.checkStatefulSetReady(ctx, ns, comp.Ref.Name)
			if err != nil {
				status.Message = err.Error()
			} else {
				status.Ready = ready
				status.Message = msg
			}

		case sleepyv1alpha1.ComponentTypeCNPGCluster:
			ready, msg, err := r.checkCNPGReady(ctx, ns, comp.Ref.Name)
			if err != nil {
				status.Message = err.Error()
			} else {
				status.Ready = ready
				status.Message = msg
			}
		}

		if !status.Ready {
			allReady = false
		}
		statuses = append(statuses, status)
	}

	hs.Status.Components = statuses

	// Update overall state based on components
	if allReady {
		if hs.Status.State != sleepyv1alpha1.StateAwake {
			hs.Status.State = sleepyv1alpha1.StateAwake
			now := metav1.Now()
			hs.Status.LastTransition = &now
		}
	} else {
		// Check if any are scaled up (waking) vs all scaled down (sleeping)
		if hs.Status.State == sleepyv1alpha1.StateAwake {
			hs.Status.State = sleepyv1alpha1.StateSleeping
			now := metav1.Now()
			hs.Status.LastTransition = &now
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) checkDeploymentReady(ctx context.Context, namespace, name string) (bool, string, error) {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &deploy); err != nil {
		return false, "", err
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	if desired == 0 {
		return false, "Scaled to 0", nil
	}

	if deploy.Status.ReadyReplicas >= desired {
		return true, fmt.Sprintf("%d/%d ready", deploy.Status.ReadyReplicas, desired), nil
	}

	return false, fmt.Sprintf("%d/%d ready", deploy.Status.ReadyReplicas, desired), nil
}

func (r *SleepyServiceReconciler) checkStatefulSetReady(ctx context.Context, namespace, name string) (bool, string, error) {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sts); err != nil {
		return false, "", err
	}

	desired := int32(1)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}

	if desired == 0 {
		return false, "Scaled to 0", nil
	}

	if sts.Status.ReadyReplicas >= desired {
		return true, fmt.Sprintf("%d/%d ready", sts.Status.ReadyReplicas, desired), nil
	}

	return false, fmt.Sprintf("%d/%d ready", sts.Status.ReadyReplicas, desired), nil
}

func (r *SleepyServiceReconciler) checkCNPGReady(ctx context.Context, namespace, name string) (bool, string, error) {
	// Use unstructured to avoid importing CNPG types
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "postgresql.cnpg.io",
		Version: "v1",
		Kind:    "Cluster",
	})

	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, cluster); err != nil {
		return false, "", err
	}

	// Check hibernation annotation
	annotations := cluster.GetAnnotations()
	if annotations != nil && annotations["cnpg.io/hibernation"] == "on" {
		return false, "Hibernated", nil
	}

	// Check status
	status, found, _ := unstructured.NestedMap(cluster.Object, "status")
	if !found {
		return false, "No status", nil
	}

	phase, _, _ := unstructured.NestedString(status, "phase")
	readyInstances, _, _ := unstructured.NestedInt64(status, "readyInstances")
	instances, _, _ := unstructured.NestedInt64(status, "instances")

	if phase == "Cluster in healthy state" && readyInstances > 0 {
		return true, fmt.Sprintf("%s (%d/%d)", phase, readyInstances, instances), nil
	}

	return false, fmt.Sprintf("%s (%d/%d)", phase, readyInstances, instances), nil
}

func (r *SleepyServiceReconciler) reconcileHibernate(ctx context.Context, hs *sleepyv1alpha1.SleepyService) (ctrl.Result, error) {
	// This would trigger hibernation - but actually the proxy handles this
	// The operator just monitors state; the proxy does the actual scaling
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SleepyServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sleepyv1alpha1.SleepyService{}).
		Named("sleepyservice").
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
