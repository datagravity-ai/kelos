package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// WebhookEventReconciler reconciles WebhookEvent objects to handle TTL-based cleanup.
type WebhookEventReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile checks whether a processed WebhookEvent has exceeded its TTL and deletes it.
func (r *WebhookEventReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var event kelosv1alpha1.WebhookEvent
	if err := r.Get(ctx, req.NamespacedName, &event); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	expired, requeueAfter := webhookEventTTLExpired(&event)
	if !expired {
		if requeueAfter > 0 {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	logger.Info("Deleting WebhookEvent due to TTL expiration", "event", event.Name)
	if err := r.Delete(ctx, &event); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// webhookEventTTLExpired checks whether a processed WebhookEvent has exceeded its TTL.
// Returns (true, 0) if the event should be deleted now, or (false, duration)
// if the event should be requeued after the given duration.
func webhookEventTTLExpired(event *kelosv1alpha1.WebhookEvent) (bool, time.Duration) {
	if event.Spec.TTLSecondsAfterProcessed == nil {
		return false, 0
	}
	if event.Status.ProcessedAt == nil {
		return false, 0
	}

	ttl := time.Duration(*event.Spec.TTLSecondsAfterProcessed) * time.Second
	expireAt := event.Status.ProcessedAt.Add(ttl)
	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return true, 0
	}
	return false, remaining
}

// SetupWithManager sets up the controller with the Manager.
func (r *WebhookEventReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelosv1alpha1.WebhookEvent{}).
		Complete(r)
}
