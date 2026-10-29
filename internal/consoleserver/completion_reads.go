package consoleserver

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
)

const completionReadAnnotationPrefix = "console.kelos.dev/read-"

func completionReadAnnotation(request *http.Request) string {
	identity, _ := request.Context().Value(principalKey{}).(principal)
	digest := sha256.Sum256([]byte(identity.username))
	return completionReadAnnotationPrefix + base64.RawURLEncoding.EncodeToString(digest[:])
}

func summarizeForUser(request *http.Request, session *kelos.Session) sessionSummary {
	summary := summarize(session)
	summary.ReadNotificationID = session.Annotations[completionReadAnnotation(request)]
	return summary
}

func (s *Server) readSessionCompletion(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	if !s.requireAccess(writer, request, access("get", "sessions", namespace, name), access("patch", "sessions", namespace, name)) {
		return
	}
	var payload struct {
		UID            string `json:"uid"`
		NotificationID string `json:"notificationId"`
	}
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if payload.UID == "" || payload.NotificationID == "" {
		writeError(writer, http.StatusBadRequest, "uid and notificationId are required")
		return
	}
	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q to mark its completion read", name), err)
		return
	}
	notification := session.Status.LastNotification
	if string(session.UID) != payload.UID || session.Annotations[sessionreset.RequestAnnotation] != "" ||
		notification == nil || notification.ID != payload.NotificationID ||
		(notification.Type != kelos.SessionNotificationTypeTurnCompleted && notification.Type != kelos.SessionNotificationTypeTurnFailed) {
		writeError(writer, http.StatusConflict, fmt.Sprintf("Session %q no longer has this completion", name))
		return
	}
	key := completionReadAnnotation(request)
	if session.Annotations[key] != notification.ID {
		original := session.DeepCopy()
		if session.Annotations == nil {
			session.Annotations = map[string]string{}
		}
		for annotation, id := range session.Annotations {
			if strings.HasPrefix(annotation, completionReadAnnotationPrefix) && id != notification.ID {
				delete(session.Annotations, annotation)
			}
		}
		session.Annotations[key] = notification.ID
		if err := s.client.Patch(request.Context(), &session, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			status := http.StatusInternalServerError
			switch {
			case apierrors.IsNotFound(err):
				status = http.StatusNotFound
			case apierrors.IsForbidden(err):
				status = http.StatusForbidden
			case apierrors.IsInvalid(err):
				status = http.StatusBadRequest
			case apierrors.IsConflict(err):
				status = http.StatusConflict
			}
			writeError(writer, status, fmt.Sprintf("marking completion read for Session %q: %v", name, err))
			return
		}
	}
	writeJSON(writer, http.StatusOK, summarizeForUser(request, &session))
}
