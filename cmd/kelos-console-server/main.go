package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
)

func main() {
	var address string
	var tokenFile string
	var defaultNamespace string
	var secureCookie bool
	var authMode string
	var proxyAuth consoleserver.ProxyAuthConfig
	flag.StringVar(&address, "bind-address", ":8080", "HTTP listen address")
	flag.StringVar(&tokenFile, "token-file", "", "Path to the static authentication token")
	flag.StringVar(&authMode, "auth-mode", consoleserver.AuthModeStaticToken, "Authentication mode: staticToken, oidc or github")
	flag.StringVar(&proxyAuth.ExternalURL, "external-url", "", "HTTPS origin of the Console")
	flag.StringVar(&proxyAuth.UsernamePrefix, "username-prefix", "", "Prefix for authenticated user identities")
	flag.StringVar(&proxyAuth.GroupsPrefix, "groups-prefix", "", "Prefix for authenticated group identities")
	flag.StringVar(&defaultNamespace, "default-namespace", "default", "Initial namespace in the Console")
	flag.BoolVar(&secureCookie, "secure-cookie", false, "Mark the authentication cookie as HTTPS-only")
	flag.Parse()

	if err := validateAuthFlags(authMode, address, tokenFile, secureCookie, proxyAuth); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}
	var token string
	if authMode == consoleserver.AuthModeStaticToken {
		var err error
		token, err = readToken(tokenFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid authentication token: %v\n", err)
			os.Exit(1)
		}
	}

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load in-cluster configuration: %v\n", err)
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	controllerClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Kubernetes client: %v\n", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Kubernetes clientset: %v\n", err)
		os.Exit(1)
	}
	config := consoleserver.Config{
		AuthMode:         authMode,
		Token:            token,
		Client:           controllerClient,
		Clientset:        clientset,
		RESTConfig:       restConfig,
		DefaultNamespace: defaultNamespace,
		SecureCookie:     secureCookie,
	}
	if authMode == consoleserver.AuthModeOIDC || authMode == consoleserver.AuthModeGitHub {
		proxyAuth.Reviewer = clientset.AuthorizationV1().SubjectAccessReviews()
		proxyAuth.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
		config.ProxyAuth = &proxyAuth
	}
	handler, err := consoleserver.New(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid server configuration: %v\n", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("Kelos Console server listening address=%s\n", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Console server failed: %v\n", err)
		os.Exit(1)
	}
}

func validateAuthFlags(mode, address, tokenFile string, secureCookie bool, proxyAuth consoleserver.ProxyAuthConfig) error {
	switch mode {
	case consoleserver.AuthModeStaticToken:
		if tokenFile == "" {
			return fmt.Errorf("--token-file is required")
		}
		if proxyAuth.ExternalURL != "" || proxyAuth.UsernamePrefix != "" || proxyAuth.GroupsPrefix != "" {
			return fmt.Errorf("proxy flags require --auth-mode=oidc or github")
		}
	case consoleserver.AuthModeOIDC, consoleserver.AuthModeGitHub:
		if tokenFile != "" || secureCookie {
			return fmt.Errorf("static token flags cannot be used with proxy authentication")
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("proxy authentication requires a loopback IP in --bind-address")
		}
	default:
		return fmt.Errorf("unsupported authentication mode %q", mode)
	}
	return nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return value, nil
}
