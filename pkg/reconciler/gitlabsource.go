/*
Copyright 2019 The Knative Authors

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
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	sourcesv1alpha1 "github.com/vincent-pli/gitlabsource/pkg/apis/sources/v1alpha1"
	clientset "github.com/vincent-pli/gitlabsource/pkg/client/clientset/versioned"
	listers "github.com/vincent-pli/gitlabsource/pkg/client/listers/sources/v1alpha1"
	"github.com/vincent-pli/gitlabsource/pkg/reconciler/resources"
	"go.uber.org/zap"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/apis/duck"
	v1beta1 "knative.dev/pkg/apis/duck/v1beta1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// controllerAgentName is the string used by this controller to identify
	// itself when creating events.
	raImageEnvVar = "GL_RA_IMAGE"
	finalizerName = controllerAgentName
)

// reconciler reconciles a GitLabSource object
type Reconciler struct {
	client          dynamic.Interface
	KubeClientSet   kubernetes.Interface
	sourceClientSet clientset.Interface
	sourceLister    listers.GitLabSourceLister
	//TODO scheme
	scheme              *runtime.Scheme
	recorder            record.EventRecorder
	logger              *zap.SugaredLogger
	receiveAdapterImage string
	webhookClient       webhookClient
}

type webhookArgs struct {
	source      *sourcesv1alpha1.GitLabSource
	domain      string
	accessToken string
	secretToken string
	projectURL  string
	hookID      string
}

// Reconcile reads that state of the cluster for a GitLabSource
// object and makes changes based on the state read and what is in the
// GitLabSource.Spec
func (r *Reconciler) Reconcile(ctx context.Context, key string) error {
	r.logger.Infof("Reconciling %v", time.Now())

	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		r.logger.Errorf("invalid resource key: %s", key)
		return nil
	}

	// Get the Pipeline Run resource with this namespace/name
	original, err := r.sourceLister.GitLabSources(namespace).Get(name)
	if errors.IsNotFound(err) {
		// The resource no longer exists, in which case we stop processing.
		r.logger.Errorf("gitlabSource %q in work queue no longer exists", key)
		return nil
	} else if err != nil {
		return err
	}

	// Don't modify the informer's copy.
	source := original.DeepCopy()

	// See if the source has been deleted
	accessor, err := meta.Accessor(source)
	if err != nil {
		r.logger.Warnf("Failed to get metadata accessor: %s", zap.Error(err))
		return err
	}

	var reconcileErr error
	if accessor.GetDeletionTimestamp() == nil {
		reconcileErr = r.reconcile(ctx, source)
	} else {
		reconcileErr = r.finalize(ctx, source)
	}

	if equality.Semantic.DeepEqual(original.Status, source.Status) {
	} else if _, err := r.updateStatus(source); err != nil {
		r.logger.Warn("Failed to update GitlabSource status", err)
		r.recorder.Event(source, corev1.EventTypeWarning, "", "gitResource failed to update")
		return err
	}

	if equality.Semantic.DeepEqual(original.Finalizers, source.Finalizers) {
	} else if _, err := r.update(source); err != nil {
		r.logger.Warn("Failed to update GitlabSource", err)
		r.recorder.Event(source, corev1.EventTypeWarning, "", "gitResource failed to update")
		return err
	}
	return reconcileErr
}

func (r *Reconciler) reconcile(ctx context.Context, source *sourcesv1alpha1.GitLabSource) error {
	source.Status.InitializeConditions()

	accessToken, err := r.secretFrom(ctx, source.Namespace, source.Spec.AccessToken.SecretKeyRef)
	if err != nil {
		source.Status.MarkNoSecrets("AccessTokenNotFound", "%s", err)
		return err
	}
	secretToken, err := r.secretFrom(ctx, source.Namespace, source.Spec.SecretToken.SecretKeyRef)
	if err != nil {
		source.Status.MarkNoSecrets("SecretTokenNotFound", "%s", err)
		return err
	}
	source.Status.MarkSecrets()

	uri, err := getSinkURI(ctx, r.client, source.Spec.Sink, source.Namespace)
	if err != nil {
		source.Status.MarkNoSink("NotFound", "%s", err)
		return err
	}
	source.Status.MarkSink(uri)

	receiver, err := r.getOwnedReceiver(ctx, source)
	if err != nil {
		if apierrors.IsNotFound(err) {
			receiver = resources.MakeReceiveAdapter(source, r.receiveAdapterImage)
			if err = controllerutil.SetControllerReference(source, receiver, r.scheme); err != nil {
				return err
			}

			_, err = r.KubeClientSet.AppsV1().Deployments(source.Namespace).Create(receiver)
			if err != nil {
				return err
			}

			service := resources.MakePublicService(source)
			if err = controllerutil.SetControllerReference(source, service, r.scheme); err != nil {
				return err
			}

			_, err = r.KubeClientSet.CoreV1().Services(source.Namespace).Create(service)

			r.recorder.Eventf(source, corev1.EventTypeNormal, "ServiceCreated", "Created Service %s", receiver.Name)
			return nil
		}
		// Error was something other than NotFound
		return err
	}

	r.addFinalizer(source)

	services, err := r.KubeClientSet.CoreV1().Services(source.Namespace).List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	svc := corev1.Service{}
	for _, service := range services.Items {
		if strings.HasPrefix(service.GetName(), source.Name) {
			svc = service
		}
	}

	receiveAdapterDomain := svc.Status.LoadBalancer.Ingress[0].IP + ":" + strconv.Itoa(int(svc.Spec.Ports[0].NodePort))
	if source.Status.WebhookIDKey == "" {
		args := &webhookArgs{
			source:      source,
			domain:      receiveAdapterDomain,
			accessToken: accessToken,
			secretToken: secretToken,
		}

		hookID, err := r.createWebhook(ctx, args)
		if err != nil {
			return err
		}
		source.Status.WebhookIDKey = hookID
	}

	return nil
}

func (r *Reconciler) finalize(ctx context.Context, source *sourcesv1alpha1.GitLabSource) error {
	// Always remove the finalizer. If there's a failure cleaning up, an event
	// will be recorded allowing the webhook to be removed manually by the
	// operator.
	r.removeFinalizer(source)

	// If a webhook was created, try to delete it
	if source.Status.WebhookIDKey != "" {
		// Get access token
		accessToken, err := r.secretFrom(ctx, source.Namespace, source.Spec.AccessToken.SecretKeyRef)
		if err != nil {
			source.Status.MarkNoSecrets("AccessTokenNotFound", "%s", err)
			r.recorder.Eventf(source, corev1.EventTypeWarning, "FailedFinalize", "Could not delete webhook %q: %v", source.Status.WebhookIDKey, err)
			return err
		}

		args := &webhookArgs{
			source:      source,
			accessToken: accessToken,
			projectURL:  source.Spec.ProjectURL,
			hookID:      source.Status.WebhookIDKey,
		}
		// Delete the webhook using the access token and stored webhook ID
		err = r.deleteWebhook(ctx, args)
		if err != nil {
			r.recorder.Eventf(source, corev1.EventTypeWarning, "FailedFinalize", "Could not delete webhook %q: %v", source.Status.WebhookIDKey, err)
			return err
		}
		// Webhook deleted, clear ID
		source.Status.WebhookIDKey = ""
	}
	return nil
}

func getProjectName(projectURL string) (string, error) {
	u, err := url.Parse(projectURL)
	if err != nil {
		return "", err
	}
	projectName := u.Path[1:]
	return projectName, nil
}

func (r *Reconciler) createWebhook(ctx context.Context, args *webhookArgs) (string, error) {
	r.logger.Info("creating GitLab webhook")

	hookOptions := &projectHookOptions{
		accessToken: args.accessToken,
		secretToken: args.secretToken,
	}

	projectName, err := getProjectName(args.source.Spec.ProjectURL)
	if err != nil {
		return "", fmt.Errorf("Failed to process project url to get the project name: " + err.Error())
	}
	hookOptions.project = projectName
	hookOptions.webhookID = args.source.Status.WebhookIDKey

	for _, event := range args.source.Spec.EventTypes {
		switch event {
		case "push_events":
			hookOptions.PushEvents = true
		case "issues_events":
			hookOptions.IssuesEvents = true
		case "confidential_issues_events":
			hookOptions.ConfidentialIssuesEvents = true
		case "merge_requests_events":
			hookOptions.MergeRequestsEvents = true
		case "tag_push_events":
			hookOptions.TagPushEvents = true
		case "pipeline_events":
			hookOptions.PipelineEvents = true
		case "wiki_page_events":
			hookOptions.WikiPageEvents = true
		case "job_events":
			hookOptions.JobEvents = true
		case "note_events":
			hookOptions.NoteEvents = true
		}
	}

	if args.source.Spec.Secure {
		hookOptions.url = "https://" + args.domain
		hookOptions.EnableSSLVerification = true
	} else {
		hookOptions.url = "http://" + args.domain
	}
	baseURL, err := getGitlabBaseURL(args.source.Spec.ProjectURL)
	if err != nil {
		return "", fmt.Errorf("Failed to process project url to get the base url: " + err.Error())
	}

	hookID, err := r.webhookClient.Create(ctx, hookOptions, baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to create webhook: %v", err)
	}
	return hookID, nil
}

func getGitlabBaseURL(projectURL string) (string, error) {
	u, err := url.Parse(projectURL)
	if err != nil {
		return "", err
	}
	projectName := u.Path[1:]
	baseurl := strings.TrimSuffix(projectURL, projectName)
	return baseurl, nil
}

func (r *Reconciler) deleteWebhook(ctx context.Context, args *webhookArgs) error {
	logger := logging.FromContext(ctx)

	logger.Info("deleting GitLab webhook")

	hookOptions := &projectHookOptions{
		accessToken: args.accessToken,
	}
	projectName, err := getProjectName(args.projectURL)
	if err != nil {
		return fmt.Errorf("Failed to process project url to get the project name: " + err.Error())
	}
	hookOptions.project = projectName
	hookOptions.webhookID = args.source.Status.WebhookIDKey
	baseURL, err := getGitlabBaseURL(args.projectURL)
	if err != nil {
		return fmt.Errorf("Failed to process project url to get the base url: " + err.Error())
	}

	err = r.webhookClient.Delete(ctx, hookOptions, baseURL)
	if err != nil {
		return fmt.Errorf("failed to delete webhook: %v", err)
	}
	return nil
}

func (r *Reconciler) secretFrom(ctx context.Context, namespace string, secretKeySelector *corev1.SecretKeySelector) (string, error) {
	secret, err := r.KubeClientSet.CoreV1().Secrets(namespace).Get(secretKeySelector.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	secretVal, ok := secret.Data[secretKeySelector.Key]
	if !ok {
		return "", fmt.Errorf(`key "%s" not found in secret "%s"`, secretKeySelector.Key, secretKeySelector.Name)
	}
	return string(secretVal), nil
}

func (r *Reconciler) getOwnedReceiver(ctx context.Context, source *sourcesv1alpha1.GitLabSource) (*v1.Deployment, error) {
	dl, err := r.KubeClientSet.AppsV1().Deployments(source.Namespace).List(metav1.ListOptions{
		LabelSelector: r.getLabelSelector(source),
		// TODO this is only needed by the fake client. Real K8s does not need it. Remove it once
		// the fake is fixed.
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.SchemeGroupVersion.String(),
			Kind:       "Deployment",
		},
	})

	if err != nil {
		logging.FromContext(ctx).Desugar().Error("Unable to list deployments: %v", zap.Error(err))
		return nil, err
	}
	for _, dep := range dl.Items {
		if metav1.IsControlledBy(&dep, source) {
			return &dep, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
}

func (r *Reconciler) addFinalizer(s *sourcesv1alpha1.GitLabSource) {
	finalizers := sets.NewString(s.Finalizers...)
	finalizers.Insert(finalizerName)
	s.Finalizers = finalizers.List()
}

func (r *Reconciler) removeFinalizer(s *sourcesv1alpha1.GitLabSource) {
	finalizers := sets.NewString(s.Finalizers...)
	finalizers.Delete(finalizerName)
	s.Finalizers = finalizers.List()
}

// GetSinkURI retrieves the sink URI from the object referenced by the given
// ObjectReference.
func getSinkURI(ctx context.Context, c dynamic.Interface, sink *corev1.ObjectReference, namespace string) (string, error) {
	if sink == nil {
		return "", fmt.Errorf("sink ref is nil")
	}

	plural, _ := meta.UnsafeGuessKindToResource(sink.GroupVersionKind())
	u, err := c.Resource(plural).Namespace(sink.Namespace).Get(sink.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	objIdentifier := fmt.Sprintf("\"%s/%s\" (%s)", u.GetNamespace(), u.GetName(), u.GroupVersionKind())

	t := v1beta1.AddressableType{}
	err = duck.FromUnstructured(u, &t)
	if err != nil {
		return "", fmt.Errorf("failed to deserialize sink %s: %v", objIdentifier, err)
	}

	if t.Status.Address == nil {
		return "", fmt.Errorf("sink %s does not contain address", objIdentifier)
	}

	if t.Status.Address.URL == nil {
		return "", fmt.Errorf("sink %s contains an empty URL", objIdentifier)
	}

	return t.Status.Address.URL.String(), nil
}

func (r *Reconciler) getLabelSelector(source *sourcesv1alpha1.GitLabSource) string {
	return fmt.Sprintf("eventing-source=%s, eventing-source-name=%s", controllerAgentName, source.Name)
}

func (r *Reconciler) updateStatus(source *sourcesv1alpha1.GitLabSource) (*sourcesv1alpha1.GitLabSource, error) {
	newSource, err := r.sourceLister.GitLabSources(source.Namespace).Get(source.Name)
	if err != nil {
		return nil, fmt.Errorf("Error getting Gitlabsourrce %s when updating status: %w", source.Name, err)
	}

	if !reflect.DeepEqual(source.Status, newSource.Status) {
		newSource.Status = source.Status
		return r.sourceClientSet.SourcesV1alpha1().GitLabSources(source.Namespace).UpdateStatus(newSource)
	}
	return newSource, nil
}

func (r *Reconciler) update(source *sourcesv1alpha1.GitLabSource) (*sourcesv1alpha1.GitLabSource, error) {
	newSource, err := r.sourceLister.GitLabSources(source.Namespace).Get(source.Name)
	if err != nil {
		return nil, fmt.Errorf("Error getting Gitlabsourrce %s when updating status: %w", source.Name, err)
	}

	if !reflect.DeepEqual(source.Finalizers, newSource.Finalizers) {
		newSource.Finalizers = source.Finalizers
		return r.sourceClientSet.SourcesV1alpha1().GitLabSources(source.Namespace).Update(newSource)
	}
	return newSource, nil
}
