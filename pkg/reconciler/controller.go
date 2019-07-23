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

package gitlabsource

import (
	"context"
	"os"
	"time"

	gitlabsourcev1alpha1 "github.com/vincent-pli/gitlabsource/pkg/apis/sources/v1alpha1"
	sourceclient "github.com/vincent-pli/gitlabsource/pkg/client/injection/client"
	sourceinformer "github.com/vincent-pli/gitlabsource/pkg/client/injection/informers/sources/v1alpha1/gitlabsource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/injection/clients/dynamicclient"
	"knative.dev/pkg/injection/clients/kubeclient"
	"knative.dev/pkg/logging"
)

const (
	resyncPeriod        = 10 * time.Hour
	controllerAgentName = "gitlab-source-controller"
)

var (
	scheme = runtime.NewScheme()
)

func NewController(
	ctx context.Context,
	cmw configmap.Watcher,
) *controller.Impl {
	logger := logging.FromContext(ctx)
	gitlabsourcev1alpha1.AddToScheme(scheme)
	cl := dynamicclient.Get(ctx)

	kubeclientset := kubeclient.Get(ctx)
	sourceclientset := sourceclient.Get(ctx)
	sourceInformer := sourceinformer.Get(ctx)

	receiveAdapterImage, defined := os.LookupEnv(raImageEnvVar)
	if !defined {
		logger.Errorf("required environment variable %q not defined", raImageEnvVar)
		return nil
	}
	// Create event broadcaster
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logger.Named("event-broadcaster").Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: controllerAgentName})

	c := &Reconciler{
		client:              cl,
		KubeClientSet:       kubeclientset,
		sourceClientSet:     sourceclientset,
		recorder:            recorder,
		scheme:              scheme,
		sourceLister:        sourceInformer.Lister(),
		receiveAdapterImage: receiveAdapterImage,
		logger:              logger,
		webhookClient:       gitLabWebhookClient{}, //TODO
	}

	impl := controller.NewImpl(c, c.logger, controllerAgentName)

	c.logger.Info("Setting up event handlers")
	sourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    impl.Enqueue,
		UpdateFunc: controller.PassNew(impl.Enqueue),
		DeleteFunc: impl.Enqueue,
	})

	return impl
}
