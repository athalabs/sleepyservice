// Copyright 2026 AthaLabs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"path/filepath"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	sleepyv1alpha1 "github.com/athalabs/sleepyservice/api/v1alpha1"
	"github.com/athalabs/sleepyservice/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(sleepyv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	config := parseFlags()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&config.zapOpts)))

	tlsOpts := setupTLSOptions(config.enableHTTP2)
	webhookCertWatcher := setupWebhookCertWatcher(config)
	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: buildWebhookTLSOpts(tlsOpts, webhookCertWatcher),
	})

	metricsServerOptions, metricsCertWatcher := setupMetricsServer(config, tlsOpts)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: config.probeAddr,
		LeaderElection:         config.enableLeaderElection,
		LeaderElectionID:       "a6c85791.atha.gr",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	setupController(mgr)
	addCertWatchersToManager(mgr, metricsCertWatcher, webhookCertWatcher)
	addHealthChecks(mgr)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

type config struct {
	metricsAddr          string
	metricsCertPath      string
	metricsCertName      string
	metricsCertKey       string
	webhookCertPath      string
	webhookCertName      string
	webhookCertKey       string
	probeAddr            string
	enableLeaderElection bool
	secureMetrics        bool
	enableHTTP2          bool
	zapOpts              zap.Options
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&cfg.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&cfg.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&cfg.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&cfg.webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&cfg.webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&cfg.webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&cfg.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&cfg.metricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	flag.StringVar(&cfg.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&cfg.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	cfg.zapOpts = zap.Options{Development: true}
	cfg.zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	return cfg
}

func setupTLSOptions(enableHTTP2 bool) []func(*tls.Config) {
	var tlsOpts []func(*tls.Config)

	if !enableHTTP2 {
		disableHTTP2 := func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		}
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	return tlsOpts
}

func setupWebhookCertWatcher(cfg config) *certwatcher.CertWatcher {
	if len(cfg.webhookCertPath) == 0 {
		return nil
	}

	setupLog.Info("Initializing webhook certificate watcher using provided certificates",
		"webhook-cert-path", cfg.webhookCertPath,
		"webhook-cert-name", cfg.webhookCertName,
		"webhook-cert-key", cfg.webhookCertKey)

	watcher, err := certwatcher.New(
		filepath.Join(cfg.webhookCertPath, cfg.webhookCertName),
		filepath.Join(cfg.webhookCertPath, cfg.webhookCertKey),
	)
	if err != nil {
		setupLog.Error(err, "Failed to initialize webhook certificate watcher")
		os.Exit(1)
	}

	return watcher
}

func buildWebhookTLSOpts(tlsOpts []func(*tls.Config), watcher *certwatcher.CertWatcher) []func(*tls.Config) {
	webhookTLSOpts := tlsOpts
	if watcher != nil {
		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = watcher.GetCertificate
		})
	}
	return webhookTLSOpts
}

func setupMetricsServer(cfg config, tlsOpts []func(*tls.Config)) (metricsserver.Options, *certwatcher.CertWatcher) {
	options := metricsserver.Options{
		BindAddress:   cfg.metricsAddr,
		SecureServing: cfg.secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if cfg.secureMetrics {
		options.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	var metricsCertWatcher *certwatcher.CertWatcher
	if len(cfg.metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", cfg.metricsCertPath,
			"metrics-cert-name", cfg.metricsCertName,
			"metrics-cert-key", cfg.metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(cfg.metricsCertPath, cfg.metricsCertName),
			filepath.Join(cfg.metricsCertPath, cfg.metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		options.TLSOpts = append(options.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	return options, metricsCertWatcher
}

func setupController(mgr ctrl.Manager) {
	operatorImage := os.Getenv("OPERATOR_IMAGE")
	if operatorImage == "" {
		setupLog.Info("OPERATOR_IMAGE not set, using default")
		operatorImage = "controller:latest"
	}

	if err := (&controller.SleepyServiceReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		OperatorImage: operatorImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SleepyService")
		os.Exit(1)
	}
}

func addCertWatchersToManager(mgr ctrl.Manager, metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher) {
	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}
}

func addHealthChecks(mgr ctrl.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}
