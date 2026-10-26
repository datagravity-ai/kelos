package consoleserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	json "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

func (s *Server) runTaskSpawner(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	if !s.requireAccess(writer, request, access("get", "taskspawners", namespace, name), access("create", "tasks", namespace, "")) {
		return
	}
	var payload struct {
		Values string `json:"values"`
	}
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("running TaskSpawner %q: %v", name, err))
		return
	}
	var values map[string]interface{}
	if strings.TrimSpace(payload.Values) != "" {
		data, err := yaml.YAMLToJSON([]byte(payload.Values))
		if err == nil {
			err = json.Unmarshal(data, &values)
		}
		if err != nil || values == nil {
			writeError(writer, http.StatusBadRequest, fmt.Sprintf("running TaskSpawner %q: values must be a YAML or JSON object", name))
			return
		}
	}
	var spawner kelos.TaskSpawner
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &spawner); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting TaskSpawner %q", name), err)
		return
	}
	task, err := taskbuilder.BuildManualTask(request.Context(), s.client, &spawner, taskbuilder.ManualTaskOptions{
		Values:     values,
		HTTPClient: s.httpClient,
		Logger:     logr.FromSlogHandler(slog.Default().Handler()).WithValues("taskspawner", name, "namespace", namespace),
	})
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.client.Create(request.Context(), task); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("creating Task %q from TaskSpawner %q", task.Name, name), err)
		return
	}
	writeJSON(writer, http.StatusCreated, consoleResourceSummary{Name: task.Name, Namespace: task.Namespace})
}
