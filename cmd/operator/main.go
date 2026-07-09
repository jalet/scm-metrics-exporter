// Command operator runs the scm-metrics-exporter Kubernetes controller-manager. It
// reconciles GitHubMetricsExporter and GitLabMetricsExporter custom resources into
// exporter Deployments (plus Service and optional ServiceMonitor).
//
// The reconcilers are registered in Epics 09 and 16; this file wires the manager,
// scheme, leader election, and health probes.
package main

import (
	"flag"
	"os"
	"path/filepath"

	// Load all in-cluster / kubeconfig auth plugins.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
	"github.com/jalet/scm-metrics-exporter/internal/controller"
)

// defaultWebhookCertDir is where controller-runtime looks for the webhook serving cert;
// the Helm chart mounts the cert-manager-issued Secret here.
const defaultWebhookCertDir = "/tmp/k8s-webhook-server/serving-certs"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(scmv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the manager's own metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the health/readiness probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election, ensuring only one active manager.")
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&zapOpts))
	ctrl.SetLogger(logger)
	// Route client-go's klog (leader election, etc.) through the same logger so all output
	// is one structured JSON stream instead of klog's separate text format.
	klog.SetLogger(logger)

	certDir := os.Getenv("WEBHOOK_CERT_DIR")
	if certDir == "" {
		certDir = defaultWebhookCertDir
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "scm-metrics-exporter.jalet.io",
		WebhookServer:          webhook.NewServer(webhook.Options{CertDir: certDir}),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// exporterImage is the image the reconciler puts in exporter Deployments; the
	// Helm chart sets it to the operator's own image by default.
	exporterImage := os.Getenv("EXPORTER_IMAGE")
	if err := (&controller.GitHubMetricsExporterReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ExporterImage: exporterImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitHubMetricsExporter")
		os.Exit(1)
	}
	if err := (&controller.GitLabMetricsExporterReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ExporterImage: exporterImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitLabMetricsExporter")
		os.Exit(1)
	}

	// Admission webhooks are always-on when serving certs are present (the Helm chart
	// mounts the cert-manager-issued Secret). Without certs -- local runs, or envtest
	// without WebhookInstallOptions -- registration is skipped by cert presence (not a
	// user toggle) so the same binary still starts.
	//nolint:gosec // certDir is operator-configured (WEBHOOK_CERT_DIR), not attacker input; this only Stats the file.
	if _, statErr := os.Stat(filepath.Join(certDir, "tls.crt")); statErr == nil {
		if err := scmv1alpha1.SetupWebhooksWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to set up admission webhooks")
			os.Exit(1)
		}
		setupLog.Info("admission webhooks registered", "certDir", certDir)
	} else {
		setupLog.Info("webhook serving certs not found; admission webhooks disabled", "certDir", certDir)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
