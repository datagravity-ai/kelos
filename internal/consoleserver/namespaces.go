package consoleserver

import (
	"net/http"
	"sort"

	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const namespaceReviewConcurrency = 8

func (s *Server) listNamespaces(writer http.ResponseWriter, request *http.Request) {
	var namespaces corev1.NamespaceList
	var options []client.ListOption
	if namespace := request.URL.Query().Get("namespace"); namespace != "" {
		options = append(options, client.MatchingFields{"metadata.name": namespace})
	}
	if err := s.client.List(request.Context(), &namespaces, options...); err != nil {
		writeError(writer, http.StatusInternalServerError, "unable to list namespaces")
		return
	}
	allowedNamespaces := make([]bool, len(namespaces.Items))
	reviews, ctx := errgroup.WithContext(request.Context())
	reviews.SetLimit(namespaceReviewConcurrency)
	reviewRequest := request.WithContext(ctx)
	for i, namespace := range namespaces.Items {
		if ctx.Err() != nil {
			break
		}
		reviews.Go(func() error {
			for _, definition := range consoleResourceDefinitions {
				if err := ctx.Err(); err != nil {
					return err
				}
				allowed, err := s.allowed(reviewRequest, access("list", definition.Resource, namespace.Name, ""))
				if err != nil {
					return err
				}
				if allowed {
					allowedNamespaces[i] = true
					break
				}
			}
			return nil
		})
	}
	if err := reviews.Wait(); err != nil || request.Context().Err() != nil {
		writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
		return
	}
	names := []string{}
	for i, allowed := range allowedNamespaces {
		if allowed {
			names = append(names, namespaces.Items[i].Name)
		}
	}
	sort.Strings(names)
	writeJSON(writer, http.StatusOK, map[string][]string{"namespaces": names})
}
