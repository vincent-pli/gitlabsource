# gitlabsource

The project is used for listener on `gitlab` webhook and redirect event received to a `addressable` application, for example:
`tekton/trigger`(https://github.com/tektoncd/triggers).

It's k8s native and implements by a k8s controller.

# Prerequisites
You must install these tools:
1. go: The language Tektoncd-pipeline-operator is built in
2. dep: For managing external Go dependencies. - Please Install dep v0.5.0 or greater.
3. [ko](https://github.com/google/ko): Build and deploy Go applications on Kubernetes

# Details
1. The project implements `Controller/reconciler` based on `injection controller` of `knative/pkg`
2. Expose applycation by loadbalance k8s `Service`, so please ensure there is extenal loadbalance installed in your env.

# Installation
1. Git clone the repo.
2. ko apply -f ./config
3. kubectl apply -f ./samples/gitlabsecret.yaml (render your own `accessToken` and `secretToken`, please goole it if not familiar)
4. kubectl apply -f ./samples/gitlabSample.yaml

