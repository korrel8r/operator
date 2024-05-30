# Makefile is self-documenting, comments starting with '##' are extracted as help text.
help: ## Display this help.
	@echo; echo = Targets =
	@grep -E '^[A-Za-Z0-9_-]+:.*##' Makefile | sed 's/:.*##\s*/#/' | column -s'#' -t
	@echo; echo  = Variables =
	@grep -E '^## [A-Z0-9_]+: ' Makefile | sed 's/^## \([A-Z0-9_]*\): \(.*\)/\1#\2/' | column -s'#' -t

## VERSION: Semantic version for release, use -dev for development pre-release versions.
VERSION?=0.1.6
## IMG_ORG: org name for images, for example quay.io/alanconway.
IMG_ORG?=$(error Set IMG_ORG to organization prefix for images, e.g. IMG_ORG=quay.io/alanconway)
## KORREL8R_VERSION: Version of korrel8r operand.
KORREL8R_VERSION=0.6.4
## KORREL8R_IMAGE: Operand image containing the korrel8r executable.
KORREL8R_IMAGE?=quay.io/korrel8r/korrel8r:$(KORREL8R_VERSION)
## NAMESPACE: Operator namespace used by `make deploy` and `make bundle-run`
NAMESPACE?=korrel8r
## IMGTOOL: May be podman or docker.
IMGTOOL?=$(shell which podman || which docker)
## ENVTEST_K8S_VERSION: version of kubebuilder for envtest testing.
ENVTEST_K8S_VERSION=1.29.x

# Names of image and bundle images.
IMG?=$(IMG_ORG)/operator
IMAGE=$(IMG):$(VERSION)
BUNDLE_IMAGE ?= $(IMG)-bundle:$(VERSION)

# Bundle options.
DEFAULT_CHANNEL ?= stable
BUNDLE_CHANNELS ?= --channels=$(DEFAULT_CHANNEL)
BUNDLE_DEFAULT_CHANNEL ?= --default-channel=$(DEFAULT_CHANNEL)
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

include .bingo/Variables.mk

##@ General

all: build test doc bundle  ## All local build & test.

push-all: all image-push bundle-push ## Build and push all images.

##@ Development

.PHONY: manifests
manifests: $(CONTROLLER_GEN) $(KUSTOMIZE) ## Generate ClusterRole and CustomResourceDefinition objects.
	cd config/default && $(KUSTOMIZE) edit set image controller=$(IMAGE)
	cd config/default && $(KUSTOMIZE) edit set namespace $(NAMESPACE)
	sed -e 's|value:.*|value: $(KORREL8R_IMAGE)|' config/default/manager_image_patch.yaml > \
		config/default/manager_image_patch.yaml_bkp && mv config/default/manager_image_patch.yaml_bkp config/default/manager_image_patch.yaml
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject methods.
	go mod tidy
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

lint: $(GOLANGCI_LINT) ## Run linters, fix code style problems.
	$(GOLANGCI_LINT) run --fix

.PHONY: test
test: manifests generate lint $(SETUP_ENVTEST) ## Run tests.
	$(shell $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p env); go test ./...

##@ Build

.PHONY: build
build: manifests generate lint ## Build manager binary.
	go build -o bin/manager main.go

run: manifests generate lint install ## Build and run a controller from your host
	cd config/overlays/rbac_for_test && $(KUSTOMIZE) edit set namespace $(NAMESPACE)
	$(KUSTOMIZE) build config/overlays/rbac_for_test | kubectl apply -f -
	KORREL8R_IMAGE=$(KORREL8R_IMAGE) go run main.go

image-build: manifests generate ## Build the manager image.
	$(IMGTOOL) build -q  -t $(IMAGE) .
image-push: image-build ## Push the manager image.
	$(IMGTOOL) push -q $(IMAGE)

##@ Deployment

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found -f -

.PHONY: deploy
deploy: install image-push ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: $(KUSTOMIZE) ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found -f -

##@ Bundle

bundle: manifests generate $(OPERATOR_SDK) ## Generate bundle manifests and metadata, then validate generated files.
	$(OPERATOR_SDK) generate kustomize manifests -q
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	$(OPERATOR_SDK) bundle validate ./bundle
	touch $@

WATCH=kubectl get events -n $(NAMESPACE) --watch-only& trap "kill %%" EXIT;

bundle-build: bundle ## Build the bundle image.
	$(IMGTOOL) build -q  -f bundle.Dockerfile -t $(BUNDLE_IMAGE) .
bundle-push: bundle-build ## Push the bundle image.
	$(IMGTOOL) push -q $(BUNDLE_IMAGE)
bundle-run: install bundle-push image-push $(OPERATOR_SDK)  ## Run the bundle image.
	$(OPERATOR_SDK) -n $(NAMESPACE) cleanup korrel8r || true
	oc create namespace $(NAMESPACE) || true
	$(WATCH) $(OPERATOR_SDK) -n $(NAMESPACE) run bundle $(BUNDLE_IMAGE) --security-context-config restricted
bundle-cleanup: $(OPERATOR_SDK)
	$(OPERATOR_SDK) -n $(NAMESPACE) cleanup korrel8r || true

.PHONY: push-all
push-all: image-push bundle-push

.PHONY: doc
doc: doc/zz_api-ref.adoc

doc/zz_api-ref.adoc: $(shell find api etc/crd-ref-docs) $(CRD_REF_DOCS)
	$(CRD_REF_DOCS) --source-path api --config etc/crd-ref-docs/config.yaml --templates-dir etc/crd-ref-docs/templates --output-path $@
GENERATED+=doc/zz_api-ref.adoc

clean:
	rm -rf bundle bundle.Dockerfile $(GENERATED) $(shell find -name zz_generated.*)

clean-cluster: bundle-cleanup undeploy	## Remove all test artifacts from the cluster.
	oc delete ns/$(NAMESPACE) || true
	oc get -o name operator | grep korrel8r | xargs -r oc delete || true
	oc delete -l app.kubernetes.io/name=korrel8r clusterrolebinding,clusterrole || true

test-deploy: clean-cluster deploy ## Deploy via kustomize and run a smoke-test
	hack/smoketest.sh

test-bundle: clean-cluster bundle-run  ## Run the bundle and run a smoke-test
	hack/smoketest.sh

resource:			## Create the default korrel8r resource
	$(KUSTOMIZE) build config/samples | kubectl -n $(NAMESPACE) apply -f -
	$(WATCH) kubectl wait -n $(NAMESPACE) --for=condition=available --timeout=60s deployment.apps/korrel8r

OPHUB?=$(error Set OPHUB to the path to your local community-operators-prod clone)
OPHUB_VERSION=$(OPHUB)/operators/korrel8r/$(VERSION)
operatorhub: bundle		## Generate modified bundle manifest for operator hub.
	mkdir -p $(OPHUB_VERSION)
	cp -aT bundle $(OPHUB_VERSION)
	echo -e '\n  # Annotations for OperatorHub\n  com.redhat.openshift.versions: "v4.10"' >> $(OPHUB_VERSION)/metadata/annotations.yaml

pre-release: ## Prepare a release commit.
	$(MAKE) test-bundle IMG_ORG=quay.io/korrel8r

release:			## Set VERISON and IMG_ORG to push release tags and images.
	$(MAKE) do-release IMG_ORG=quay.io/korrel8r

do-release: push-all
	hack/tag-release.sh $(VERSION) $(TAG_FLAGS)
	$(IMGTOOL) push $(IMAGE) $(IMG):latest
	$(IMGTOOL) push $(BUNDLE_IMAGE) $(IMG)-bundle:latest

tools: $(BINGO) ## Download all tools needed for development
	$(BINGO) get
