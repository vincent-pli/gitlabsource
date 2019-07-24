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

package resources

import (
	"fmt"

	sourcesv1alpha1 "github.com/vincent-pli/gitlabsource/pkg/apis/sources/v1alpha1"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func MakeReceiveAdapter(source *sourcesv1alpha1.GitLabSource, receiveAdapterImage string) *v1.Deployment {
	replicas := int32(1)
	labels := map[string]string{
		"eventing-source":      "gitlab-source-controller",
		"eventing-source-name": source.Name,
	}
	sinkURI := source.Status.SinkURI
	env := []corev1.EnvVar{
		{
			Name: "GITLAB_SECRET_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: source.Spec.SecretToken.SecretKeyRef,
			},
		},
		{
			Name:  "SINK",
			Value: sinkURI,
		},
	}
	containerArgs := []string{fmt.Sprintf("--sink=%s", sinkURI)}

	return &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", source.Name),
			Namespace:    source.Namespace,
			Labels:       labels,
		},
		Spec: v1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"sidecar.istio.io/inject": "true",
					},
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: source.Spec.ServiceAccountName,
					Containers: []corev1.Container{
						{
							Name:  "receive-adapter",
							Image: receiveAdapterImage,
							Env:   env,
							Args:  containerArgs,
						},
					},
				},
			},
		},
	}
}

func MakePublicService(source *sourcesv1alpha1.GitLabSource) *corev1.Service {
	labels := map[string]string{
		"eventing-source":      "gitlab-source-controller",
		"eventing-source-name": source.Name,
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", source.Name),
			Namespace:    source.Namespace,
			Labels:       labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Protocol:   corev1.ProtocolTCP,
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
			Selector: labels,
			Type:     corev1.ServiceTypeNodePort,
		},
	}
}
