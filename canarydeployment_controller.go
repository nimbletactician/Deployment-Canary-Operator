/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	deploymentv1alpha1 "github.com/example/canary-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// CanaryDeploymentReconciler reconciles a CanaryDeployment object
type CanaryDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder is used to generate events
	Recorder record.EventRecorder
	// PromClient is used to query Prometheus metrics
	PromClient promv1.API
}

// +kubebuilder:rbac:groups=deployment.k8s.io,resources=canarydeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=deployment.k8s.io,resources=canarydeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=deployment.k8s.io,resources=canarydeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is the main reconciliation loop for CanaryDeployment resources
// It manages the lifecycle of canary deployments and their traffic shifting
func (r *CanaryDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Reconciling CanaryDeployment", "namespace", req.Namespace, "name", req.Name)

	// Fetch the CanaryDeployment resource
	canary := &deploymentv1alpha1.CanaryDeployment{}
	if err := r.Get(ctx, req.NamespacedName, canary); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted
			log.Info("CanaryDeployment resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		log.Error(err, "Failed to get CanaryDeployment")
		return ctrl.Result{}, err
	}

	// Initialize status if needed
	if canary.Status.Phase == "" {
		canary.Status.Phase = deploymentv1alpha1.CanaryPhaseInitializing
		canary.Status.CurrentStep = 0
		canary.Status.CurrentWeight = 0
		canary.Status.StartedAt = &metav1.Time{Time: time.Now()}

		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status")
			return ctrl.Result{}, err
		}

		// Create event for initialization
		r.Recorder.Event(canary, corev1.EventTypeNormal, "Initializing", "Started canary deployment process")

		return ctrl.Result{Requeue: true}, nil
	}

	// Check for completion
	if canary.Status.Phase == deploymentv1alpha1.CanaryPhaseSucceeded ||
		canary.Status.Phase == deploymentv1alpha1.CanaryPhaseFailed ||
		canary.Status.Phase == deploymentv1alpha1.CanaryPhaseCancelled {
		log.Info("CanaryDeployment has completed", "phase", canary.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Get the primary deployment
	primaryDeploy := &appsv1.Deployment{}
	primaryNamespacedName := types.NamespacedName{
		Name:      canary.Spec.PrimaryDeployment,
		Namespace: canary.Namespace,
	}
	if err := r.Get(ctx, primaryNamespacedName, primaryDeploy); err != nil {
		log.Error(err, "Failed to get primary deployment", "deployment", canary.Spec.PrimaryDeployment)
		r.Recorder.Event(canary, corev1.EventTypeWarning, "GetPrimaryDeploymentFailed",
			fmt.Sprintf("Failed to get primary deployment %s: %v", canary.Spec.PrimaryDeployment, err))
		return ctrl.Result{}, err
	}

	// Check if canary deployment exists, create if not
	canaryDeploy := &appsv1.Deployment{}
	canaryDeployName := fmt.Sprintf("%s-canary", canary.Spec.PrimaryDeployment)
	canaryNamespacedName := types.NamespacedName{
		Name:      canaryDeployName,
		Namespace: canary.Namespace,
	}

	canaryExists := true
	if err := r.Get(ctx, canaryNamespacedName, canaryDeploy); err != nil {
		if errors.IsNotFound(err) {
			canaryExists = false
		} else {
			log.Error(err, "Failed to get canary deployment")
			return ctrl.Result{}, err
		}
	}

	// Create canary deployment if it doesn't exist
	if !canaryExists {
		log.Info("Creating canary deployment", "deployment", canaryDeployName)
		canaryDeploy = r.buildCanaryDeployment(canary, primaryDeploy)
		if err := controllerutil.SetControllerReference(canary, canaryDeploy, r.Scheme); err != nil {
			log.Error(err, "Failed to set controller reference for canary deployment")
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, canaryDeploy); err != nil {
			log.Error(err, "Failed to create canary deployment")
			r.Recorder.Event(canary, corev1.EventTypeWarning, "CreateCanaryDeploymentFailed",
				fmt.Sprintf("Failed to create canary deployment: %v", err))
			return ctrl.Result{}, err
		}

		// Update status with canary deployment name
		canary.Status.CanaryDeploymentName = canaryDeployName
		canary.Status.Phase = deploymentv1alpha1.CanaryPhaseProgressing
		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(canary, corev1.EventTypeNormal, "CanaryDeploymentCreated",
			fmt.Sprintf("Created canary deployment %s", canaryDeployName))

		return ctrl.Result{Requeue: true}, nil
	}

	// Get the service
	service := &corev1.Service{}
	serviceNamespacedName := types.NamespacedName{
		Name:      canary.Spec.Service,
		Namespace: canary.Namespace,
	}
	if err := r.Get(ctx, serviceNamespacedName, service); err != nil {
		log.Error(err, "Failed to get service", "service", canary.Spec.Service)
		r.Recorder.Event(canary, corev1.EventTypeWarning, "GetServiceFailed",
			fmt.Sprintf("Failed to get service %s: %v", canary.Spec.Service, err))
		return ctrl.Result{}, err
	}

	// Handle the current step
	currentStep := int(canary.Status.CurrentStep)
	if currentStep >= len(canary.Spec.Steps) {
		// All steps completed, promote the canary if auto-promote is enabled
		if canary.Spec.AutoPromote {
			log.Info("Auto-promoting canary deployment")
			if err := r.promoteCanary(ctx, canary, primaryDeploy, canaryDeploy); err != nil {
				log.Error(err, "Failed to promote canary deployment")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		} else {
			// Wait for manual promotion
			if canary.Status.Phase != deploymentv1alpha1.CanaryPhaseAnalyzing {
				canary.Status.Phase = deploymentv1alpha1.CanaryPhaseAnalyzing
				if err := r.Status().Update(ctx, canary); err != nil {
					log.Error(err, "Failed to update CanaryDeployment status")
					return ctrl.Result{}, err
				}
				r.Recorder.Event(canary, corev1.EventTypeNormal, "CanaryAnalyzing",
					"All steps completed, canary deployment is ready for manual promotion")
			}
			return ctrl.Result{}, nil
		}
	}

	// Get the current step
	step := canary.Spec.Steps[currentStep]

	// Check if we need to update the traffic weight
	if canary.Status.CurrentWeight != step.Weight {
		log.Info("Updating traffic weight", "weight", step.Weight)
		if err := r.updateTrafficWeight(ctx, canary, service, primaryDeploy, canaryDeploy, step.Weight); err != nil {
			log.Error(err, "Failed to update traffic weight")
			return ctrl.Result{}, err
		}

		// Update status
		canary.Status.CurrentWeight = step.Weight
		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(canary, corev1.EventTypeNormal, "TrafficWeightUpdated",
			fmt.Sprintf("Updated canary traffic weight to %d%%", step.Weight))

		// Parse pause duration and requeue
		pause, err := time.ParseDuration(step.Pause)
		if err != nil {
			log.Error(err, "Failed to parse pause duration", "pause", step.Pause)
			return ctrl.Result{}, err
		}

		// Wait for the specified pause duration before proceeding
		log.Info("Pausing before next step", "duration", pause)
		return ctrl.Result{RequeueAfter: pause}, nil
	}

	// Analyze metrics if analysis is configured
	if canary.Spec.Analysis != nil && canary.Status.Phase == deploymentv1alpha1.CanaryPhaseProgressing {
		canary.Status.Phase = deploymentv1alpha1.CanaryPhaseAnalyzing
		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status")
			return ctrl.Result{}, err
		}

		// Check metrics
		success, metricResults, err := r.analyzeMetrics(ctx, canary)
		if err != nil {
			log.Error(err, "Failed to analyze metrics")
			return ctrl.Result{}, err
		}

		// Update metric results
		canary.Status.MetricResults = metricResults
		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status with metric results")
			return ctrl.Result{}, err
		}

		if !success {
			// Metrics failed, rollback or limit weight
			if step.Weight > canary.Spec.Analysis.MaxWeight {
				log.Info("Metrics failed, rolling back canary deployment")
				canary.Status.Phase = deploymentv1alpha1.CanaryPhaseFailed
				canary.Status.CompletedAt = &metav1.Time{Time: time.Now()}
				if err := r.Status().Update(ctx, canary); err != nil {
					log.Error(err, "Failed to update CanaryDeployment status")
					return ctrl.Result{}, err
				}

				r.Recorder.Event(canary, corev1.EventTypeWarning, "MetricCheckFailed",
					"Metric check failed, rolling back canary deployment")

				return ctrl.Result{}, nil
			}

			// Continue with limited weight
			r.Recorder.Event(canary, corev1.EventTypeWarning, "MetricCheckFailed",
				fmt.Sprintf("Metric check failed, limiting weight to %d%%", canary.Spec.Analysis.MaxWeight))
		}

		// Move to the next step
		canary.Status.CurrentStep++
		canary.Status.Phase = deploymentv1alpha1.CanaryPhaseProgressing
		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(canary, corev1.EventTypeNormal, "StepCompleted",
			fmt.Sprintf("Completed step %d, proceeding to next step", currentStep))

		return ctrl.Result{Requeue: true}, nil
	}

	// Move to the next step (if no analysis or already progressing)
	if canary.Status.Phase == deploymentv1alpha1.CanaryPhaseProgressing {
		canary.Status.CurrentStep++
		if err := r.Status().Update(ctx, canary); err != nil {
			log.Error(err, "Failed to update CanaryDeployment status")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(canary, corev1.EventTypeNormal, "StepCompleted",
			fmt.Sprintf("Completed step %d, proceeding to next step", currentStep))

		return ctrl.Result{Requeue: true}, nil
	}

	// Default requeue
	log.Info("Requeuing for analysis", "phase", canary.Status.Phase)

	// If analyzing, parse the analysis interval and requeue
	if canary.Status.Phase == deploymentv1alpha1.CanaryPhaseAnalyzing && canary.Spec.Analysis != nil {
		interval, err := time.ParseDuration(canary.Spec.Analysis.Interval)
		if err != nil {
			log.Error(err, "Failed to parse analysis interval", "interval", canary.Spec.Analysis.Interval)
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: interval}, nil
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// buildCanaryDeployment creates a new canary deployment based on the primary deployment
func (r *CanaryDeploymentReconciler) buildCanaryDeployment(canary *deploymentv1alpha1.CanaryDeployment, primary *appsv1.Deployment) *appsv1.Deployment {
	// Create a new deployment based on the primary, but with canary container images
	canaryDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-canary", primary.Name),
			Namespace: primary.Namespace,
			Labels:    map[string]string{},
		},
		Spec: *primary.Spec.DeepCopy(),
	}

	// Copy labels from primary deployment and add canary-specific labels
	for k, v := range primary.Labels {
		canaryDeploy.Labels[k] = v
	}
	canaryDeploy.Labels["app.kubernetes.io/part-of"] = primary.Name
	canaryDeploy.Labels["app.kubernetes.io/name"] = fmt.Sprintf("%s-canary", primary.Name)

	// Set the canary image
	for i := range canaryDeploy.Spec.Template.Spec.Containers {
		// Update the container image only for the main container (typically the first one)
		if i == 0 {
			canaryDeploy.Spec.Template.Spec.Containers[i].Image = canary.Spec.CanaryImage
		}
	}

	// Add canary-specific labels to the pod template
	if canaryDeploy.Spec.Template.Labels == nil {
		canaryDeploy.Spec.Template.Labels = map[string]string{}
	}
	canaryDeploy.Spec.Template.Labels["app.kubernetes.io/part-of"] = primary.Name
	canaryDeploy.Spec.Template.Labels["app.kubernetes.io/name"] = fmt.Sprintf("%s-canary", primary.Name)
	canaryDeploy.Spec.Template.Labels["canary"] = "true"

	// Start with minimal replica count - we'll scale up gradually with traffic
	var minReplicas int32 = 1
	canaryDeploy.Spec.Replicas = &minReplicas

	// Parse the maxSurge value from the canary spec
	if canary.Spec.MaxSurge != "" {
		// If maxSurge is a percentage, calculate the actual value
		if strings.HasSuffix(canary.Spec.MaxSurge, "%") {
			percentStr := strings.TrimSuffix(canary.Spec.MaxSurge, "%")
			percent, err := strconv.Atoi(percentStr)
			if err == nil && percent > 0 {
				// Calculate maxSurge based on primary deployment's replicas
				primaryReplicas := int(*primary.Spec.Replicas)
				maxSurgeValue := (primaryReplicas * percent) / 100
				if maxSurgeValue < 1 {
					maxSurgeValue = 1
				}
				maxSurgeIntOrStr := intstr.FromInt(maxSurgeValue)

				// Set strategy
				canaryDeploy.Spec.Strategy = appsv1.DeploymentStrategy{
					Type: appsv1.RollingUpdateDeploymentStrategyType,
					RollingUpdate: &appsv1.RollingUpdateDeployment{
						MaxSurge: &maxSurgeIntOrStr,
					},
				}
			}
		} else {
			// If maxSurge is an absolute value
			maxSurgeIntOrStr := intstr.Parse(canary.Spec.MaxSurge)
			canaryDeploy.Spec.Strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge: &maxSurgeIntOrStr,
				},
			}
		}
	}

	return canaryDeploy
}

// updateTrafficWeight updates the traffic weight for the canary deployment
func (r *CanaryDeploymentReconciler) updateTrafficWeight(
	ctx context.Context,
	canary *deploymentv1alpha1.CanaryDeployment,
	service *corev1.Service,
	primaryDeploy *appsv1.Deployment,
	canaryDeploy *appsv1.Deployment,
	weight int32,
) error {
	log := log.FromContext(ctx)

	// Calculate the appropriate replica counts based on weight
	primaryReplicas := *primaryDeploy.Spec.Replicas

	// Calculate canary replicas based on weight
	totalWeight := int32(100)
	var canaryReplicas int32 = 1

	if weight > 0 {
		// Calculate replicas proportionally, ensuring at least 1 replica
		canaryReplicas = (primaryReplicas * weight) / totalWeight
		if canaryReplicas < 1 {
			canaryReplicas = 1
		}
	}

	// Update the canary deployment's replica count
	canaryDeploy.Spec.Replicas = &canaryReplicas
	if err := r.Update(ctx, canaryDeploy); err != nil {
		log.Error(err, "Failed to update canary deployment replicas")
		return err
	}

	// Update service selectors or weights as needed
	// This implementation assumes a service mesh or similar infrastructure for traffic splitting
	// Common implementations include:
	// 1. Istio VirtualService for traffic splitting
	// 2. Nginx Ingress with canary annotations
	// 3. Custom header-based routing

	// The example below shows a simplified approach using service selector manipulation
	// In a production environment, you would likely use a service mesh integration

	// Clone the service to avoid modifying the original
	updatedService := service.DeepCopy()

	// Update the service's annotations to indicate the canary weight
	if updatedService.Annotations == nil {
		updatedService.Annotations = map[string]string{}
	}
	updatedService.Annotations["canary.deployment.k8s.io/weight"] = fmt.Sprintf("%d", weight)

	// For a real implementation, you would integrate with your traffic management solution here
	// For example, with Istio:
	// - Create or update a VirtualService to split traffic between primary and canary
	// - Set the weight for each destination according to the current step

	if err := r.Update(ctx, updatedService); err != nil {
		log.Error(err, "Failed to update service annotations")
		return err
	}

	return nil
}

// promoteCanary promotes the canary deployment by updating the primary deployment with the canary image
func (r *CanaryDeploymentReconciler) promoteCanary(
	ctx context.Context,
	canary *deploymentv1alpha1.CanaryDeployment,
	primaryDeploy *appsv1.Deployment,
	canaryDeploy *appsv1.Deployment,
) error {
	log := log.FromContext(ctx)
	log.Info("Promoting canary deployment", "canary", canaryDeploy.Name, "primary", primaryDeploy.Name)

	// Update status to promoting
	canary.Status.Phase = deploymentv1alpha1.CanaryPhasePromoting
	if err := r.Status().Update(ctx, canary); err != nil {
		log.Error(err, "Failed to update CanaryDeployment status")
		return err
	}

	// Update the primary deployment with the canary image
	primaryDeployCopy := primaryDeploy.DeepCopy()

	// Update container image in the primary deployment to match the canary
	for i := range primaryDeployCopy.Spec.Template.Spec.Containers {
		if i == 0 {
			primaryDeployCopy.Spec.Template.Spec.Containers[i].Image = canary.Spec.CanaryImage
		}
	}

	// Apply the update to the primary deployment
	if err := r.Update(ctx, primaryDeployCopy); err != nil {
		log.Error(err, "Failed to update primary deployment with canary image")
		r.Recorder.Event(canary, corev1.EventTypeWarning, "PromotionFailed",
			fmt.Sprintf("Failed to promote canary: %v", err))
		return err
	}

	// Reset service traffic to 100% primary
	service := &corev1.Service{}
	serviceNamespacedName := types.NamespacedName{
		Name:      canary.Spec.Service,
		Namespace: canary.Namespace,
	}
	if err := r.Get(ctx, serviceNamespacedName, service); err != nil {
		log.Error(err, "Failed to get service for traffic reset")
		return err
	}

	// Update service annotations to reset traffic routing
	serviceCopy := service.DeepCopy()
	if serviceCopy.Annotations != nil {
		delete(serviceCopy.Annotations, "canary.deployment.k8s.io/weight")
	}

	if err := r.Update(ctx, serviceCopy); err != nil {
		log.Error(err, "Failed to update service annotations")
		return err
	}

	// Mark the canary deployment as successful
	canary.Status.Phase = deploymentv1alpha1.CanaryPhaseSucceeded
	canary.Status.CompletedAt = &metav1.Time{Time: time.Now()}
	if err := r.Status().Update(ctx, canary); err != nil {
		log.Error(err, "Failed to update CanaryDeployment status")
		return err
	}

	r.Recorder.Event(canary, corev1.EventTypeNormal, "PromotionSucceeded",
		"Successfully promoted canary deployment to primary")

	return nil
}

// analyzeMetrics checks the metrics defined in the canary analysis
func (r *CanaryDeploymentReconciler) analyzeMetrics(
	ctx context.Context,
	canary *deploymentv1alpha1.CanaryDeployment,
) (bool, []deploymentv1alpha1.MetricResult, error) {
	log := log.FromContext(ctx)
	log.Info("Analyzing metrics", "canary", canary.Name)

	// Check if Prometheus client is initialized
	if r.PromClient == nil {
		return false, nil, fmt.Errorf("Prometheus client not initialized")
	}

	// Get metrics from the analysis spec or step
	var metrics []deploymentv1alpha1.MetricCheck
	currentStep := int(canary.Status.CurrentStep)

	// Check if there are metrics defined for the current step
	if currentStep < len(canary.Spec.Steps) && len(canary.Spec.Steps[currentStep].Metrics) > 0 {
		metrics = canary.Spec.Steps[currentStep].Metrics
	} else if canary.Spec.Analysis != nil {
		// Otherwise, use the global metrics from the analysis
		metrics = canary.Spec.Analysis.Metrics
	}

	// If no metrics are defined, consider the analysis successful
	if len(metrics) == 0 {
		return true, nil, nil
	}

	results := make([]deploymentv1alpha1.MetricResult, 0, len(metrics))
	allSuccessful := true

	// Execute each metric query and check the results
	for _, metric := range metrics {
		result := deploymentv1alpha1.MetricResult{
			Name:      metric.Name,
			Timestamp: metav1.Time{Time: time.Now()},
			Success:   false,
		}

		// Execute Prometheus query
		val, _, err := r.PromClient.Query(ctx, metric.PrometheusQuery, time.Now())
		if err != nil {
			log.Error(err, "Failed to execute Prometheus query", "query", metric.PrometheusQuery)
			result.Success = false
			result.Message = fmt.Sprintf("Query execution failed: %v", err)
			results = append(results, result)
			allSuccessful = false
			continue
		}

		// Check if there's no data
		if val.Type() == model.ValNone {
			log.Info("No data returned from query", "query", metric.PrometheusQuery)
			result.Success = false
			result.Message = "No data returned from query"
			results = append(results, result)
			allSuccessful = false
			continue
		}

		// Process the result based on the value type
		switch val.Type() {
		case model.ValVector:
			// For vector results (most common)
			vector := val.(model.Vector)
			if len(vector) == 0 {
				result.Success = false
				result.Message = "No data points in vector result"
				results = append(results, result)
				allSuccessful = false
				continue
			}

			// Get the first value (typically what we care about)
			value := float64(vector[0].Value)
			result.Value = fmt.Sprintf("%f", value)

			// Check threshold
			threshold, err := strconv.ParseFloat(metric.Threshold, 64)
			if err != nil {
				log.Error(err, "Failed to parse threshold", "threshold", metric.Threshold)
				result.Success = false
				result.Message = fmt.Sprintf("Invalid threshold format: %v", err)
				results = append(results, result)
				allSuccessful = false
				continue
			}

			// Check against threshold range if provided
			if metric.ThresholdRange != nil {
				min := math.Inf(-1)
				max := math.Inf(1)

				if metric.ThresholdRange.Min != "" {
					minVal, err := strconv.ParseFloat(metric.ThresholdRange.Min, 64)
					if err == nil {
						min = minVal
					}
				}

				if metric.ThresholdRange.Max != "" {
					maxVal, err := strconv.ParseFloat(metric.ThresholdRange.Max, 64)
					if err == nil {
						max = maxVal
					}
				}

				result.Success = value >= min && value <= max
				if !result.Success {
					result.Message = fmt.Sprintf("Value %f is outside range [%f, %f]", value, min, max)
				}
			} else {
				// Simple threshold check
				result.Success = value <= threshold
				if !result.Success {
					result.Message = fmt.Sprintf("Value %f exceeds threshold %f", value, threshold)
				}
			}

		case model.ValScalar:
			// For scalar results
			scalar := val.(*model.Scalar)
			value := float64(scalar.Value)
			result.Value = fmt.Sprintf("%f", value)

			threshold, err := strconv.ParseFloat(metric.Threshold, 64)
			if err != nil {
				result.Success = false
				result.Message = fmt.Sprintf("Invalid threshold format: %v", err)
			} else {
				result.Success = value <= threshold
				if !result.Success {
					result.Message = fmt.Sprintf("Value %f exceeds threshold %f", value, threshold)
				}
			}

		default:
			result.Success = false
			result.Message = fmt.Sprintf("Unsupported result type: %s", val.Type().String())
		}

		results = append(results, result)
		if !result.Success {
			allSuccessful = false
		}
	}

	return allSuccessful, results, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *CanaryDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize Prometheus client if not already initialized
	if r.PromClient == nil {
		// In a real implementation, this would be configured via environment variables or configuration
		promConfig := api.Config{
			Address: "http://prometheus-server.monitoring:9090",
		}

		client, err := api.NewClient(promConfig)
		if err != nil {
			return fmt.Errorf("failed to create Prometheus client: %w", err)
		}

		r.PromClient = promv1.NewAPI(client)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&deploymentv1alpha1.CanaryDeployment{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
