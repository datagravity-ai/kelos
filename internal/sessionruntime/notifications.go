package sessionruntime

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func (j *Journal) notificationID(eventID int64) string {
	return fmt.Sprintf("%s/%d", j.journalID, eventID)
}

// LatestNotification returns the latest retained event that needs attention.
func (j *Journal) LatestNotification() *kelos.SessionNotification {
	j.mu.Lock()
	defer j.mu.Unlock()
	resolved := make(map[string]bool)
	resolvedRequest := false
	for i := len(j.events) - 1; i >= j.firstEvent; i-- {
		event := j.events[i]
		var kind kelos.SessionNotificationType
		switch event.Type {
		case EventInputResolved:
			resolved[event.InputID] = true
			continue
		case EventInputRequested:
			if resolved[event.InputID] {
				resolvedRequest = true
				continue
			}
			kind = kelos.SessionNotificationTypeInputRequested
		case EventTurnCompleted:
			// A resolved input request supersedes outcomes from earlier turns.
			if resolvedRequest {
				return nil
			}
			switch event.Status {
			case "completed":
				kind = kelos.SessionNotificationTypeTurnCompleted
			case "failed":
				kind = kelos.SessionNotificationTypeTurnFailed
			default:
				return nil
			}
		default:
			continue
		}
		if event.Timestamp == nil {
			return nil
		}
		return &kelos.SessionNotification{
			ID:        j.notificationID(event.ID),
			Type:      kind,
			EventTime: metav1.NewTime(*event.Timestamp),
		}
	}
	return nil
}
