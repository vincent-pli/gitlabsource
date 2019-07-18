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
	"time"

	"github.com/knative/pkg/configmap"
	"github.com/knative/pkg/controller"
	"github.com/knative/pkg/injection/clients/kubeclient"
	"github.com/knative/pkg/logging"
	"github.com/knative/pkg/tracker"
	sourceclient "github.com/vincent-pli/gitlabsource/pkg/client/injection/client"
	sourceinformer "github.com/vincent-pli/gitlabsource/pkg/client/injection/informers/sources/v1alpha1/gitlabsource"
	"github.com/vincent-pli/gitlabsource/pkg/reconciler"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

const (
	resyncPeriod = 10 * time.Hour
	controllerAgentName = "gitlab-source-controller"
)

func NewController(
	ctx context.Context,
	cmw configmap.Watcher,
) *controller.Impl {
	logger := logging.FromContext(ctx)
	kubeclientset := kubeclient.Get(ctx)
	sourceclientset := sourceclient.Get(ctx)
	sourceInformer := sourceinformer.Get(ctx)

	receiveAdapterImage, defined := os.LookupEnv(raImageEnvVar)
	if !defined {
		return fmt.Errorf("required environment variable %q not defined", raImageEnvVar)
	}

	c := &Reconciler{
		KubeClientSet:  kubeclientset,
		sourceClientSet:  sourceclientset,
		recorder:       createRecord(),     
		scheme:   
		sourceLister:  sourceInformer.Lister(),      
		receiveAdapterImage: receiveAdapterImage,
		logger:        logger,
		webhookClient:       gitLabWebhookClient{}, //TODO
	},

	impl := controller.NewImpl(c, c.Logger, controllerAgentName)

	c.Logger.Info("Setting up event handlers")
	sourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    impl.Enqueue,
		UpdateFunc: controller.PassNew(impl.Enqueue),
		DeleteFunc: impl.Enqueue,
	})

	return impl
}

func createRecord() record.EventRecorder {
	// Create event broadcaster
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logger.Named("event-broadcaster").Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: opt.KubeClientSet.CoreV1().Events("")})

	recorder = eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	 
	return recorder
}
