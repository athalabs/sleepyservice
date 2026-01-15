// Copyright 2026 AthaLabs
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"strings"
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

// dependencyGraph represents the dependency relationships between components
type dependencyGraph struct {
	// dependencies maps each component name to the list of components it depends on
	dependencies map[string][]string
	// dependents maps each component name to the list of components that depend on it
	dependents map[string][]string
	// components is the set of all component names
	components map[string]bool
	// componentsByName maps name to the full Component for reference
	componentsByName map[string]sleepyv1alpha1.Component
}

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

	var hs sleepyv1alpha1.SleepyService
	if err := r.Get(ctx, req.NamespacedName, &hs); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !hs.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &hs)
	}

	if err := r.ensureFinalizer(ctx, &hs); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureProxyResources(ctx, &hs); err != nil {
		log.Error(err, "Failed to ensure proxy resources")
		return ctrl.Result{}, err
	}

	if err := r.updateComponentStatuses(ctx, &hs); err != nil {
		log.Error(err, "Failed to update component statuses")
		return ctrl.Result{}, err
	}

	r.updateBackendServiceStatus(&hs)

	if err := r.reconcileState(ctx, &hs); err != nil {
		log.Error(err, "Failed to reconcile state")
		return ctrl.Result{}, err
	}

	log.Info("State after reconcileState", "state", hs.Status.State, "desiredState", hs.Status.DesiredState)

	if err := r.Status().Update(ctx, &hs); err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	// Requeue quickly when waking to poll for component readiness
	// This must come before checkIdleTimeout so it takes priority
	if hs.Status.State == sleepyv1alpha1.StateWaking {
		log.Info("Requeuing for component readiness check", "state", hs.Status.State)
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
	}

	if result, shouldReturn := r.checkIdleTimeout(ctx, &hs); shouldReturn {
		return result, nil
	}

	return ctrl.Result{}, nil
}

func (r *SleepyServiceReconciler) ensureFinalizer(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	if !controllerutil.ContainsFinalizer(hs, finalizerName) {
		controllerutil.AddFinalizer(hs, finalizerName)
		if err := r.Update(ctx, hs); err != nil {
			return err
		}
	}
	return nil
}

func (r *SleepyServiceReconciler) ensureProxyResources(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	if err := r.ensureProxyServiceAccount(ctx, hs); err != nil {
		return fmt.Errorf("failed to ensure proxy service account: %w", err)
	}

	if err := r.ensureProxyRole(ctx, hs); err != nil {
		return fmt.Errorf("failed to ensure proxy role: %w", err)
	}

	if err := r.ensureProxyRoleBinding(ctx, hs); err != nil {
		return fmt.Errorf("failed to ensure proxy role binding: %w", err)
	}

	if err := r.ensureBackendService(ctx, hs); err != nil {
		return fmt.Errorf("failed to ensure backend service: %w", err)
	}

	if err := r.ensureProxyDeployment(ctx, hs); err != nil {
		return fmt.Errorf("failed to ensure proxy deployment: %w", err)
	}

	if err := r.ensureProxyService(ctx, hs); err != nil {
		return fmt.Errorf("failed to ensure proxy service: %w", err)
	}

	return nil
}

func (r *SleepyServiceReconciler) updateBackendServiceStatus(hs *sleepyv1alpha1.SleepyService) {
	if hs.Spec.BackendService != nil {
		backendSvcName := fmt.Sprintf("%s-actual", hs.Name)
		hs.Status.BackendService = backendSvcName
	}
}

func (r *SleepyServiceReconciler) checkIdleTimeout(ctx context.Context, hs *sleepyv1alpha1.SleepyService) (ctrl.Result, bool) {
	log := log.FromContext(ctx)

	if hs.Spec.IdleTimeout.Duration <= 0 || hs.Status.State != sleepyv1alpha1.StateAwake {
		return ctrl.Result{}, false
	}

	if hs.Status.LastActivity == nil {
		return ctrl.Result{}, false
	}

	idleDuration := time.Since(hs.Status.LastActivity.Time)
	if idleDuration > hs.Spec.IdleTimeout.Duration {
		log.Info("Idle timeout reached, hibernating")
		result, _ := r.reconcileHibernate(ctx, hs)
		return result, true
	}

	requeueAfter := hs.Spec.IdleTimeout.Duration - idleDuration
	return ctrl.Result{RequeueAfter: requeueAfter}, true
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
			// Check if this is a dependency validation error
			log.Error(err, "Failed to scale up components")
			// For now, return the error which will be retried
			// In the future, could set State = StateError for permanent failures
			return err
		}
		hs.Status.State = sleepyv1alpha1.StateWaking
		now := metav1.Now()
		hs.Status.LastTransition = &now

	case currentState == sleepyv1alpha1.StateWaking:
		// Currently waking - continue scaling with dependencies
		// This ensures we progress through dependency levels on each reconcile
		if err := r.scaleUpComponents(ctx, hs); err != nil {
			log.Error(err, "Failed to scale up components during waking")
			return err
		}

		// Check if all components are ready
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

// buildDependencyGraph creates a dependency graph from the components list
func (r *SleepyServiceReconciler) buildDependencyGraph(components []sleepyv1alpha1.Component) (*dependencyGraph, error) {
	graph := &dependencyGraph{
		dependencies:     make(map[string][]string),
		dependents:       make(map[string][]string),
		components:       make(map[string]bool),
		componentsByName: make(map[string]sleepyv1alpha1.Component),
	}

	// First pass: register all component names
	for _, comp := range components {
		if graph.components[comp.Name] {
			return nil, fmt.Errorf("duplicate component name: %s", comp.Name)
		}
		graph.components[comp.Name] = true
		graph.componentsByName[comp.Name] = comp
		graph.dependencies[comp.Name] = []string{}
		graph.dependents[comp.Name] = []string{}
	}

	// Second pass: build dependency edges
	for _, comp := range components {
		for _, dep := range comp.DependsOn {
			// Validate dependency exists
			if !graph.components[dep] {
				return nil, fmt.Errorf("component '%s' depends on '%s' which does not exist", comp.Name, dep)
			}
			// Add forward edge (comp depends on dep)
			graph.dependencies[comp.Name] = append(graph.dependencies[comp.Name], dep)
			// Add reverse edge (dep is depended on by comp)
			graph.dependents[dep] = append(graph.dependents[dep], comp.Name)
		}
	}

	// Validate no circular dependencies
	if err := graph.validateDependencies(); err != nil {
		return nil, err
	}

	return graph, nil
}

// validateDependencies checks for circular dependencies using DFS
func (g *dependencyGraph) validateDependencies() error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := []string{}

	var dfs func(node string) error
	dfs = func(node string) error {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		for _, dep := range g.dependencies[node] {
			if !visited[dep] {
				if err := dfs(dep); err != nil {
					return err
				}
			} else if recStack[dep] {
				// Found a cycle - build cycle path for error message
				cycleStart := -1
				for i, comp := range path {
					if comp == dep {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cyclePath := append(path[cycleStart:], dep)
					return fmt.Errorf("circular dependency detected: %v", cyclePath)
				}
				return fmt.Errorf("circular dependency detected involving: %s", dep)
			}
		}

		recStack[node] = false
		path = path[:len(path)-1]
		return nil
	}

	for comp := range g.components {
		if !visited[comp] {
			if err := dfs(comp); err != nil {
				return err
			}
		}
	}

	return nil
}

// computeScalingLevels performs topological sort using Kahn's algorithm
// Returns components grouped into levels where each level can be scaled in parallel
func (g *dependencyGraph) computeScalingLevels() [][]string {
	// Calculate in-degree for each component (number of dependencies)
	inDegree := make(map[string]int)
	for comp := range g.components {
		inDegree[comp] = len(g.dependencies[comp])
	}

	// Start with components that have no dependencies
	levels := [][]string{}
	queue := []string{}

	for comp := range g.components {
		if inDegree[comp] == 0 {
			queue = append(queue, comp)
		}
	}

	// Process components level by level
	for len(queue) > 0 {
		// Current level is all components in the queue
		currentLevel := make([]string, len(queue))
		copy(currentLevel, queue)
		levels = append(levels, currentLevel)

		// Clear queue for next level
		queue = []string{}

		// For each component in current level, reduce in-degree of its dependents
		for _, comp := range currentLevel {
			for _, dependent := range g.dependents[comp] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					queue = append(queue, dependent)
				}
			}
		}
	}

	return levels
}

// areComponentsReady checks if all specified components are ready
func (r *SleepyServiceReconciler) areComponentsReady(hs *sleepyv1alpha1.SleepyService, componentNames []string) bool {
	// Build a map of component statuses for quick lookup
	statusMap := make(map[string]sleepyv1alpha1.ComponentStatus)
	for _, status := range hs.Status.Components {
		statusMap[status.Name] = status
	}

	// Check if all components are ready
	for _, name := range componentNames {
		status, exists := statusMap[name]
		if !exists || !status.Ready {
			return false
		}
	}

	return true
}

// scaleUpComponents scales up components in dependency order
func (r *SleepyServiceReconciler) scaleUpComponents(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	log := log.FromContext(ctx)

	// Build and validate dependency graph
	graph, err := r.buildDependencyGraph(hs.Spec.Components)
	if err != nil {
		return fmt.Errorf("failed to build dependency graph: %w", err)
	}

	// Compute scaling levels using topological sort
	levels := graph.computeScalingLevels()
	log.Info("Computed scaling levels", "numLevels", len(levels), "levels", levels)

	// Determine which level we should be working on
	// We can scale level N if all components in levels 0..N-1 are ready
	currentLevel := -1
	for i := range levels {
		if i == 0 {
			// Always can work on level 0
			currentLevel = 0
		} else {
			// Check if previous level is ready
			prevLevelComponents := levels[i-1]
			if r.areComponentsReady(hs, prevLevelComponents) {
				currentLevel = i
			} else {
				// Previous level not ready, wait
				log.Info("Waiting for previous level to be ready", "level", i-1, "components", prevLevelComponents)
				break
			}
		}
	}

	if currentLevel == -1 {
		// No level to scale (shouldn't happen, but handle gracefully)
		return nil
	}

	// Scale components in the current level
	componentsToScale := levels[currentLevel]
	log.Info("Scaling level", "level", currentLevel, "components", componentsToScale)

	for _, compName := range componentsToScale {
		comp := graph.componentsByName[compName]

		ns := comp.Ref.Namespace
		if ns == "" {
			ns = hs.Namespace
		}

		var err error
		switch comp.Type {
		case sleepyv1alpha1.ComponentTypeDeployment:
			err = r.scaleUpDeployment(ctx, ns, comp)
		case sleepyv1alpha1.ComponentTypeStatefulSet:
			err = r.scaleUpStatefulSet(ctx, ns, comp)
		case sleepyv1alpha1.ComponentTypeCNPGCluster:
			err = r.scaleUpCNPGCluster(ctx, ns, comp)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) scaleUpDeployment(ctx context.Context, namespace string, comp sleepyv1alpha1.Component) error {
	log := log.FromContext(ctx)

	replicas := int32(1)
	if comp.Replicas != nil {
		replicas = *comp.Replicas
	}

	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: namespace}, &deploy); err != nil {
		return fmt.Errorf("failed to get Deployment %s: %w", comp.Ref.Name, err)
	}

	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas == 0 {
		log.Info("Scaling up Deployment", "name", comp.Ref.Name, "replicas", replicas)
		deploy.Spec.Replicas = &replicas
		if err := r.Update(ctx, &deploy); err != nil {
			return fmt.Errorf("failed to scale up Deployment %s: %w", comp.Ref.Name, err)
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) scaleUpStatefulSet(ctx context.Context, namespace string, comp sleepyv1alpha1.Component) error {
	log := log.FromContext(ctx)

	replicas := int32(1)
	if comp.Replicas != nil {
		replicas = *comp.Replicas
	}

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: namespace}, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s: %w", comp.Ref.Name, err)
	}

	if sts.Spec.Replicas == nil || *sts.Spec.Replicas == 0 {
		log.Info("Scaling up StatefulSet", "name", comp.Ref.Name, "replicas", replicas)
		sts.Spec.Replicas = &replicas
		if err := r.Update(ctx, &sts); err != nil {
			return fmt.Errorf("failed to scale up StatefulSet %s: %w", comp.Ref.Name, err)
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) scaleUpCNPGCluster(ctx context.Context, namespace string, comp sleepyv1alpha1.Component) error {
	log := log.FromContext(ctx)

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "postgresql.cnpg.io",
		Version: "v1",
		Kind:    "Cluster",
	})

	if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: namespace}, cluster); err != nil {
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

	return nil
}

func (r *SleepyServiceReconciler) scaleDownComponents(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	for _, comp := range hs.Spec.Components {
		ns := comp.Ref.Namespace
		if ns == "" {
			ns = hs.Namespace
		}

		var err error
		switch comp.Type {
		case sleepyv1alpha1.ComponentTypeDeployment:
			err = r.scaleDownDeployment(ctx, ns, comp)
		case sleepyv1alpha1.ComponentTypeStatefulSet:
			err = r.scaleDownStatefulSet(ctx, ns, comp)
		case sleepyv1alpha1.ComponentTypeCNPGCluster:
			err = r.scaleDownCNPGCluster(ctx, ns, comp)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func (r *SleepyServiceReconciler) scaleDownDeployment(ctx context.Context, namespace string, comp sleepyv1alpha1.Component) error {
	log := log.FromContext(ctx)

	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: namespace}, &deploy); err != nil {
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

	return nil
}

func (r *SleepyServiceReconciler) scaleDownStatefulSet(ctx context.Context, namespace string, comp sleepyv1alpha1.Component) error {
	log := log.FromContext(ctx)

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: namespace}, &sts); err != nil {
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

	return nil
}

func (r *SleepyServiceReconciler) scaleDownCNPGCluster(ctx context.Context, namespace string, comp sleepyv1alpha1.Component) error {
	log := log.FromContext(ctx)

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "postgresql.cnpg.io",
		Version: "v1",
		Kind:    "Cluster",
	})

	if err := r.Get(ctx, types.NamespacedName{Name: comp.Ref.Name, Namespace: namespace}, cluster); err != nil {
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
							Name:            "proxy",
							Image:           r.OperatorImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"/proxy"},
							Ports:           r.buildProxyContainerPorts(hs),
							Env:             r.buildProxyEnv(hs),
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
		{Name: "WAKE_TIMEOUT", Value: hs.Spec.WakeTimeout.Duration.String()},
		{Name: "IDLE_TIMEOUT", Value: hs.Spec.IdleTimeout.Duration.String()},
	}

	if appComponent != nil {
		// Determine backend service name
		var backendSvcName string
		if hs.Spec.BackendService != nil {
			// New API: use managed backend service
			backendSvcName = fmt.Sprintf("%s-actual", hs.Name)
		} else {
			// Old API: use deployment name as service name
			backendSvcName = appComponent.Ref.Name
		}

		// Build backend host (FQDN without port)
		backendHost := fmt.Sprintf("%s.%s", backendSvcName, hs.Namespace)

		// Build backend ports list for multi-port support
		var backendPorts []string
		if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Ports) > 0 {
			for _, p := range hs.Spec.BackendService.Ports {
				backendPorts = append(backendPorts, fmt.Sprintf("%d", p.Port))
			}
		} else {
			// Default to port 80
			backendPorts = []string{"80"}
		}

		env = append(env,
			corev1.EnvVar{Name: "DEPLOYMENT_NAME", Value: appComponent.Ref.Name},
			corev1.EnvVar{Name: "BACKEND_HOST", Value: backendHost},
			corev1.EnvVar{Name: "BACKEND_PORTS", Value: strings.Join(backendPorts, ",")},
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
			Ports: r.buildProxyServicePorts(hs),
		}

		return nil
	})

	return err
}

func (r *SleepyServiceReconciler) ensureBackendService(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	log := log.FromContext(ctx)

	if r.isBackendServiceDisabled(hs) {
		log.Info("Backend Service creation disabled")
		return nil
	}

	appComponent, err := r.findAppComponent(hs)
	if err != nil {
		return err
	}

	podSelector, err := r.getPodSelector(ctx, hs, appComponent)
	if err != nil {
		return err
	}

	backendSvcName := fmt.Sprintf("%s-actual", hs.Name)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backendSvcName,
			Namespace: hs.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(hs, svc, r.Scheme); err != nil {
			return err
		}

		r.configureBackendServiceAnnotations(svc, hs)
		r.configureBackendServiceSpec(svc, hs, podSelector)

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create/update backend Service: %w", err)
	}

	log.Info("Backend Service ensured", "name", backendSvcName)
	return nil
}

func (r *SleepyServiceReconciler) isBackendServiceDisabled(hs *sleepyv1alpha1.SleepyService) bool {
	return hs.Spec.BackendService != nil && hs.Spec.BackendService.Enabled != nil && !*hs.Spec.BackendService.Enabled
}

func (r *SleepyServiceReconciler) findAppComponent(hs *sleepyv1alpha1.SleepyService) (*sleepyv1alpha1.Component, error) {
	var appComponent *sleepyv1alpha1.Component
	for i := range hs.Spec.Components {
		if hs.Spec.Components[i].Type == sleepyv1alpha1.ComponentTypeDeployment ||
			hs.Spec.Components[i].Type == sleepyv1alpha1.ComponentTypeStatefulSet {
			appComponent = &hs.Spec.Components[i]
		}
	}

	if appComponent == nil {
		return nil, fmt.Errorf("no Deployment or StatefulSet component found")
	}

	return appComponent, nil
}

func (r *SleepyServiceReconciler) getPodSelector(ctx context.Context, hs *sleepyv1alpha1.SleepyService, appComponent *sleepyv1alpha1.Component) (map[string]string, error) {
	ns := appComponent.Ref.Namespace
	if ns == "" {
		ns = hs.Namespace
	}

	var podSelector map[string]string
	var err error

	if appComponent.Type == sleepyv1alpha1.ComponentTypeDeployment {
		podSelector, err = r.getDeploymentPodSelector(ctx, ns, appComponent.Ref.Name)
	} else {
		podSelector, err = r.getStatefulSetPodSelector(ctx, ns, appComponent.Ref.Name)
	}

	if err != nil {
		return nil, err
	}

	if len(podSelector) == 0 {
		return nil, fmt.Errorf("no pod selector labels found on component")
	}

	return podSelector, nil
}

func (r *SleepyServiceReconciler) getDeploymentPodSelector(ctx context.Context, namespace, name string) (map[string]string, error) {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &deploy); err != nil {
		return nil, fmt.Errorf("failed to get Deployment: %w", err)
	}
	return deploy.Spec.Selector.MatchLabels, nil
}

func (r *SleepyServiceReconciler) getStatefulSetPodSelector(ctx context.Context, namespace, name string) (map[string]string, error) {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &sts); err != nil {
		return nil, fmt.Errorf("failed to get StatefulSet: %w", err)
	}
	return sts.Spec.Selector.MatchLabels, nil
}

func (r *SleepyServiceReconciler) configureBackendServiceAnnotations(svc *corev1.Service, hs *sleepyv1alpha1.SleepyService) {
	if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Annotations) > 0 {
		if svc.Annotations == nil {
			svc.Annotations = make(map[string]string)
		}
		for k, v := range hs.Spec.BackendService.Annotations {
			svc.Annotations[k] = v
		}
	}
}

func (r *SleepyServiceReconciler) configureBackendServiceSpec(svc *corev1.Service, hs *sleepyv1alpha1.SleepyService, podSelector map[string]string) {
	svc.Spec.Selector = podSelector
	svc.Spec.Type = r.getBackendServiceType(hs)
	svc.Spec.Ports = r.buildBackendServicePorts(hs)

	if hs.Spec.BackendService != nil {
		r.applyOptionalServiceFields(svc, hs.Spec.BackendService)
	}
}

func (r *SleepyServiceReconciler) getBackendServiceType(hs *sleepyv1alpha1.SleepyService) corev1.ServiceType {
	if hs.Spec.BackendService != nil && hs.Spec.BackendService.Type != "" {
		return hs.Spec.BackendService.Type
	}
	return corev1.ServiceTypeClusterIP
}

func (r *SleepyServiceReconciler) applyOptionalServiceFields(svc *corev1.Service, backendSvc *sleepyv1alpha1.BackendServiceSpec) {
	if backendSvc.ClusterIP != "" {
		svc.Spec.ClusterIP = backendSvc.ClusterIP
	}
	if len(backendSvc.ExternalIPs) > 0 {
		svc.Spec.ExternalIPs = backendSvc.ExternalIPs
	}
	if backendSvc.LoadBalancerIP != "" {
		svc.Spec.LoadBalancerIP = backendSvc.LoadBalancerIP
	}
	if backendSvc.SessionAffinity != "" {
		svc.Spec.SessionAffinity = backendSvc.SessionAffinity
	}
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

func (r *SleepyServiceReconciler) buildProxyContainerPorts(hs *sleepyv1alpha1.SleepyService) []corev1.ContainerPort {
	// If user specified ports explicitly, expose all of them on the proxy container
	if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Ports) > 0 {
		ports := make([]corev1.ContainerPort, len(hs.Spec.BackendService.Ports))
		for i, p := range hs.Spec.BackendService.Ports {
			ports[i] = corev1.ContainerPort{
				Name:          p.Name,
				ContainerPort: p.Port,
			}
		}
		return ports
	}

	// Default to port 80 if nothing specified
	return []corev1.ContainerPort{
		{
			Name:          "http",
			ContainerPort: 80,
		},
	}
}

func (r *SleepyServiceReconciler) buildProxyServicePorts(hs *sleepyv1alpha1.SleepyService) []corev1.ServicePort {
	// If user specified ports explicitly, expose all of them on the proxy
	if hs.Spec.BackendService != nil && len(hs.Spec.BackendService.Ports) > 0 {
		ports := make([]corev1.ServicePort, len(hs.Spec.BackendService.Ports))
		for i, p := range hs.Spec.BackendService.Ports {
			// Proxy service exposes the same port and forwards to the same port on the proxy container
			// This allows the proxy to know which backend port to forward to based on which port it receives on
			protocol := corev1.ProtocolTCP
			if p.Protocol != "" {
				protocol = p.Protocol
			}

			ports[i] = corev1.ServicePort{
				Name:       p.Name,
				Protocol:   protocol,
				Port:       p.Port,
				TargetPort: intstr.FromInt32(p.Port),
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

//nolint:unparam
func (r *SleepyServiceReconciler) updateComponentStatuses(ctx context.Context, hs *sleepyv1alpha1.SleepyService) error {
	statuses := make([]sleepyv1alpha1.ComponentStatus, 0, len(hs.Spec.Components))
	allReady := true

	// Build dependency graph for status messaging
	graph, graphErr := r.buildDependencyGraph(hs.Spec.Components)
	var levels [][]string
	if graphErr == nil {
		levels = graph.computeScalingLevels()
	}

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

		// Add dependency-aware messaging when not ready
		if !status.Ready && graphErr == nil && len(comp.DependsOn) > 0 {
			// Build status map for dependency lookup
			statusMap := make(map[string]sleepyv1alpha1.ComponentStatus)
			for _, s := range statuses {
				statusMap[s.Name] = s
			}

			// Check if any dependencies are not ready
			notReadyDeps := []string{}
			for _, dep := range comp.DependsOn {
				if depStatus, exists := statusMap[dep]; exists && !depStatus.Ready {
					notReadyDeps = append(notReadyDeps, dep)
				}
			}

			// Update message if waiting on dependencies
			if len(notReadyDeps) > 0 {
				status.Message = fmt.Sprintf("Waiting for dependencies: %v. Current: %s", notReadyDeps, status.Message)
			}
		}

		// Add level information during waking state
		if hs.Status.State == sleepyv1alpha1.StateWaking && graphErr == nil {
			for levelIdx, levelComponents := range levels {
				for _, compName := range levelComponents {
					if compName == comp.Name {
						status.Message = fmt.Sprintf("[Level %d/%d] %s", levelIdx, len(levels)-1, status.Message)
						break
					}
				}
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
