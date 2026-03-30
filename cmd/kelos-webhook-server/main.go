package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		source        string
		port          int
		namespace     string
		allNamespaces bool
		metricsAddr   string
		healthAddr    string
	)

	flag.StringVar(&source, "source", "", "Webhook source type (required): github, linear")
	flag.IntVar(&port, "port", 8080, "Port to listen for webhooks on")
	flag.StringVar(&namespace, "namespace", "", "Namespace to watch for TaskSpawners (default: all namespaces)")
	flag.BoolVar(&allNamespaces, "all-namespaces", false, "Watch TaskSpawners in all namespaces")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":9090", "Address for Prometheus metrics")
	flag.StringVar(&healthAddr, "health-probe-bind-address", ":8081", "Address for health probes")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	logger := zap.New(zap.UseFlagOptions(opts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("webhook-server")

	// Validate required flags
	if source == "" {
		log.Error(fmt.Errorf("--source is required"), "Invalid flags")
		os.Exit(1)
	}

	if source != "github" && source != "linear" {
		log.Error(fmt.Errorf("--source must be github or linear, got %q", source), "Invalid flags")
		os.Exit(1)
	}

	// Handle namespace configuration
	if namespace != "" && allNamespaces {
		log.Error(fmt.Errorf("conflicting flags"), "--namespace and --all-namespaces cannot both be set")
		os.Exit(1)
	}
	if namespace == "" && !allNamespaces {
		allNamespaces = true
		log.Info("No namespace specified, watching all namespaces")
	}

	secretBytes := []byte(os.Getenv("WEBHOOK_SECRET"))
	if len(secretBytes) == 0 {
		log.Info("No WEBHOOK_SECRET provided, webhook signature validation will be disabled")
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "Unable to get kubeconfig")
		os.Exit(1)
	}

	// Setup cache options for namespace filtering
	cacheOpts := cache.Options{}
	if !allNamespaces {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{
			namespace: {},
		}
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthAddr,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		Cache:                  cacheOpts,
		// Disable leader election since we're stateless
		LeaderElection: false,
	})
	if err != nil {
		log.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	handler := &webhook.Handler{
		Client: mgr.GetClient(),
		Log:    log.WithName("webhook").WithValues("source", source),
		Source: source,
		Secret: secretBytes,
	}

	// Register health and readiness endpoints
	if err := mgr.AddHealthzCheck("healthz", func(req *http.Request) error {
		return nil // Always healthy
	}); err != nil {
		log.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", func(req *http.Request) error {
		var list kelosv1alpha1.TaskSpawnerList
		if err := mgr.GetClient().List(req.Context(), &list, client.Limit(1)); err != nil {
			return fmt.Errorf("unable to list TaskSpawners: %w", err)
		}
		return nil
	}); err != nil {
		log.Error(err, "Unable to set up readiness check")
		os.Exit(1)
	}

	// Register webhook HTTP server as a Runnable
	srv := &webhookServer{
		handler: handler,
		port:    port,
		log:     log,
	}
	if err := mgr.Add(srv); err != nil {
		log.Error(err, "Unable to add webhook server runnable")
		os.Exit(1)
	}

	log.Info("Starting webhook server",
		"source", source,
		"port", port,
		"namespace", namespace,
		"allNamespaces", allNamespaces,
		"secretConfigured", len(secretBytes) > 0)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited with error")
		os.Exit(1)
	}
}

// webhookServer implements manager.Runnable for the HTTP webhook listener.
type webhookServer struct {
	handler *webhook.Handler
	port    int
	log     logr.Logger
}

// Start starts the HTTP server and blocks until the context is cancelled.
func (s *webhookServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Main webhook endpoint
	mux.Handle(fmt.Sprintf("/webhook/%s", s.handler.Source), s.handler)

	// Health endpoints (also available via manager's probe server)
	mux.Handle("/healthz", webhook.HealthHandler())
	mux.Handle("/readyz", webhook.ReadyHandler(s.handler.Client))

	// Root endpoint with basic info
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"service":"kelos-webhook-server","source":"%s","ready":true}`, s.handler.Source)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
		// Security settings
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		s.log.Info("HTTP server listening", "port", s.port, "source", s.handler.Source)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error(err, "HTTP server error")
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	s.log.Info("Shutting down HTTP server")

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Error(err, "HTTP server shutdown error")
		return err
	}

	return nil
}

// NeedLeaderElection returns false since the webhook server is stateless.
func (s *webhookServer) NeedLeaderElection() bool {
	return false
}
