/*
Copyright 2019 The Tekton Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"log"
	"os"

	"knative.dev/pkg/configmap"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/logging/logkey"
	"knative.dev/pkg/signals"
	"knative.dev/pkg/webhook"

	//"github.com/tektoncd/triggers/pkg/apis/triggers/v1alpha1"
	"github.com/vincent-pli/gitlabsource/pkg/apis/sources/v1alpha1"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// WebhookLogKey is the name of the logger for the webhook cmd
const (
	WebhookLogKey = "webhook"
	// ConfigName is the name of the ConfigMap that the logging config will be stored in
	ConfigName            = "config-logging-sources"
	SystemNamespaceEnvVar = "SYSTEM_NAMESPACE"
	DefaultNamespace      = "tekton-sources"
)

func main() {
	flag.Parse()
	cm, err := configmap.Load("/etc/config-logging")
	if err != nil {
		log.Fatalf("Error loading logging configuration: %v", err)
	}
	config, err := logging.NewConfigFromMap(cm)
	if err != nil {
		log.Fatalf("Error parsing logging configuration: %v", err)
	}
	logger, atomicLevel := logging.NewLoggerFromConfig(config, WebhookLogKey)

	defer func() {
		err := logger.Sync()
		if err != nil {
			logger.Fatal("Failed to sync the logger", zap.Error(err))
		}
	}()

	logger = logger.With(zap.String(logkey.ControllerType, "webhook"))

	logger.Info("Starting the Configuration Webhook")

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	clusterConfig, err := rest.InClusterConfig()
	if err != nil {
		logger.Fatal("Failed to get in cluster config", zap.Error(err))
	}

	kubeClient, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		logger.Fatal("Failed to get the client set", zap.Error(err))
	}
	// Watch the logging config map and dynamically update logging levels.
	configMapWatcher := configmap.NewInformedWatcher(kubeClient, getNamespace())
	configMapWatcher.Watch(ConfigName, logging.UpdateLevelFromConfigMap(logger, atomicLevel, WebhookLogKey))
	if err = configMapWatcher.Start(stopCh); err != nil {
		logger.Fatalf("failed to start configuration manager: %v", err)
	}

	options := webhook.ControllerOptions{
		ServiceName:    "tekton-sources-webhook",
		DeploymentName: "tekton-sources-webhook",
		Namespace:      getNamespace(),
		Port:           8443,
		SecretName:     "sources-webhook-certs",
		WebhookName:    "sources-webhook.tekton.dev",
	}
	//TODO add validations here
	controller := webhook.AdmissionController{
		Client:  kubeClient,
		Options: options,
		Handlers: map[schema.GroupVersionKind]webhook.GenericCRD{
			v1alpha1.SchemeGroupVersion.WithKind("GitLabSource"): &v1alpha1.GitLabSource{},
		},
		Logger:                logger,
		DisallowUnknownFields: true,
	}

	if err := controller.Run(stopCh); err != nil {
		logger.Fatal("Error running admission controller", zap.Error(err))
	}

}

// GetNamespace holds the K8s namespace where our system components run.
func getNamespace() string {
	systemNamespace := os.Getenv(SystemNamespaceEnvVar)
	if systemNamespace == "" {
		return DefaultNamespace
	}
	return systemNamespace
}
