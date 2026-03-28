package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func int32Ptr(i int32) *int32 { return &i }

func TestWebhookEventTTLExpired(t *testing.T) {
	now := metav1.Now()
	pastTime := metav1.NewTime(now.Add(-2 * time.Hour))
	futureTime := metav1.NewTime(now.Add(1 * time.Hour))

	tests := []struct {
		name           string
		event          *kelosv1alpha1.WebhookEvent
		wantExpired    bool
		wantRequeueGt0 bool
	}{
		{
			name: "No TTL set",
			event: &kelosv1alpha1.WebhookEvent{
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &now,
				},
			},
			wantExpired:    false,
			wantRequeueGt0: false,
		},
		{
			name: "Not yet processed",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
				},
			},
			wantExpired:    false,
			wantRequeueGt0: false,
		},
		{
			name: "TTL expired",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
				},
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &pastTime,
				},
			},
			wantExpired:    true,
			wantRequeueGt0: false,
		},
		{
			name: "TTL not yet expired",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(3600),
				},
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &futureTime,
				},
			},
			wantExpired:    false,
			wantRequeueGt0: true,
		},
		{
			name: "Zero TTL expires immediately",
			event: &kelosv1alpha1.WebhookEvent{
				Spec: kelosv1alpha1.WebhookEventSpec{
					TTLSecondsAfterProcessed: int32Ptr(0),
				},
				Status: kelosv1alpha1.WebhookEventStatus{
					ProcessedAt: &now,
				},
			},
			wantExpired:    true,
			wantRequeueGt0: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, requeueAfter := webhookEventTTLExpired(tt.event)
			if expired != tt.wantExpired {
				t.Errorf("expired = %v, want %v", expired, tt.wantExpired)
			}
			if tt.wantRequeueGt0 && requeueAfter <= 0 {
				t.Errorf("expected positive requeue duration, got %v", requeueAfter)
			}
			if !tt.wantRequeueGt0 && requeueAfter > 0 {
				t.Errorf("expected zero requeue duration, got %v", requeueAfter)
			}
		})
	}
}
