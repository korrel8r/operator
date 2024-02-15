# Makefile is self-documenting, comments starting with '##' are extracted as help text.
help: ## Display this help.
	@echo; echo = Targets =
	@grep -E '^[A-Za-Z0-9_-]+:.*##' Makefile | sed 's/:.*##\s*/#/' | column -s'#' -t
	@echo; echo  = Variables =
	@grep -E '^## [A-Z0-9_]+: ' Makefile | sed 's/^## \([A-Z0-9_]*\): \(.*\)/\1#\2/' | column -s'#' -t

## VERSION: Semantic version for release. Use a -dev[N] suffix for work in progress.
VERSION?=0.0.8
## IMG: Base name of image to build or deploy, without version tag.
IMG?=quay.io/korrel8r/operator
## KORREL8R_IMAGE: Operand image containing the korrel8r executable.
KORREL8R_IMAGE?=quay.io/korrel8r/korrel8r:v0.5.8
## NAMESPACE: Operator namespace used by `make deploy` and `make bundle-run`
NAMESPACE?=korrel8r
## IMGTOOL: May be podman or docker.
IMGTOOL?=$(shell which podman || which docker)

# Full name of manager image
IMAGE=$(IMG):$(VERSION)

# Bundle options.
DEFAULT_CHANNEL ?= stable
BUNDLE_CHANNELS ?= --channels=$(DEFAULT_CHANNEL)
BUNDLE_DEFAULT_CHANNEL ?= --default-channel=$(DEFAULT_CHANNEL)
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# BUNDLE_IMAGE defines the image:tag used for the bundle.
BUNDLE_IMAGE ?= $(IMG)-bundle:$(VERSION)

# BUNDLE_GEN_FLAGS are the flags passed to the operator-sdk generate bundle command
BUNDLE_GEN_FLAGS ?= -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)

# Set the Operator SDK version to use. By default, what is installed on the system is used.
# This is useful for CI or a project to utilize a specific version of the operator-sdk toolkit.
OPERATOR_SDK_VERSION ?= v1.33.0

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.26.0

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

##@ General

all: build test doc bundle  ## All local build & test.

push-all: all image-push bundle-push ## Build and push all images.

##@ Development

.PHONY: manifests
manifests: $(MAKEFILES) controller-gen kustomize ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMAGE)
	cd config/default && $(KUSTOMIZE) edit set namespace $(NAMESPACE)
	sed -i 's|value:.*|value: $(KORREL8R_IMAGE)|' config/default/manager_image_patch.yaml
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

go.mod: $(find -name *.go)
	go mod tidy

.PHONY: lint
lint: golangci-lint ## Run the linter to find and fix code style problems.
	$(GOLANGCI_LINT) run --fix

.PHONY: test
test: manifests generate lint envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: go.mod manifests generate lint ## Build manager binary.
	go build -o bin/manager main.go

run: go.mod manifests generate lint install ## Run a controller from your host.
	KORREL8R_IMAGE=$(KORREL8R_IMAGE) go run main.go

image-build:  ## Build the manager image.
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
deploy: install manifests image-push ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found -f -

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT=$(LOCALBIN)/golangci-lint
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk

## Tool Versions
KUSTOMIZE_VERSION ?= v5.3.0
CONTROLLER_TOOLS_VERSION ?= v0.13.0

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: operator-sdk
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
operator-sdk: ## Download operator-sdk locally if necessary.
ifeq (,$(wildcard $(OPERATOR_SDK)))
ifeq (, $(shell which operator-sdk 2>/dev/null))
	@{ \
	set -e ;\
	mkdir -p $(dir $(OPERATOR_SDK)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH} ;\
	chmod +x $(OPERATOR_SDK) ;\
	}
else
OPERATOR_SDK = $(shell which operator-sdk)
endif
endif

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest


bundle: manifests operator-sdk ## Generate bundle manifests and metadata, then validate generated files.
	$(OPERATOR_SDK) generate kustomize manifests -q
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle $(BUNDLE_GEN_FLAGS)
	$(OPERATOR_SDK) bundle validate ./bundle
	touch $@

WATCH=kubectl get events -n $(NAMESPACE) --watch-only& trap "kill %%" EXIT;

bundle-build: bundle ## Build the bundle image.
	$(IMGTOOL) build -q  -f bundle.Dockerfile -t $(BUNDLE_IMAGE) .
bundle-push: bundle-build ## Push the bundle image.
	$(IMGTOOL) push -q $(BUNDLE_IMAGE)
bundle-run: install bundle-push image-push operator-sdk  ## Run the bundle image.
	$(OPERATOR_SDK) -n $(NAMESPACE) cleanup korrel8r || true
	oc create namespace $(NAMESPACE) || true
	$(WATCH) $(OPERATOR_SDK) -n $(NAMESPACE) run bundle $(BUNDLE_IMAGE)
bundle-cleanup: operator-sdk
	$(OPERATOR_SDK) -n $(NAMESPACE) cleanup korrel8r || true

.PHONY: push-all
push-all: image-push bundle-push

push-latest: push-all
	docker push $(IMAGE) $(IMG):latest
	docker push $(BUNDLE_IMAGE) $(IMG)-bundle:latest

.PHONY: doc
doc: doc/zz_api-ref.adoc

doc/zz_api-ref.adoc: $(shell find api -name '*.go') $(shell find doc/crd-ref-docs) bin/crd-ref-docs
	bin/crd-ref-docs --source-path api --config doc/crd-ref-docs/config.yaml --templates-dir doc/crd-ref-docs/templates --output-path $@

bin/crd-ref-docs:
	GOBIN=$(PWD)/bin go install github.com/elastic/crd-ref-docs@latest

clean:
	rm -rf bunde bundle.Dockerfile

clean-cluster: bundle-cleanup undeploy	## Remove all test artifacts from the cluster.
	oc delete ns/$(NAMESPACE) || true
	oc get -o name operator | grep korrel8r | xargs -r oc delete || true

test-deploy: clean-cluster deploy ## Deploy via kustoize and run a smoke-test
	hack/smoketest.sh

test-bundle: clean-cluster bundle-run  ## Run the bundle and run a smoke-test
	hack/smoketest.sh

OPHUB?=$(error Set OPHUB to the path to your local community-operators-prod clone)
OPHUB_VERSION=$(OPHUB)/operators/korrel8r/$(VERSION)
operatorhub: bundle		## Generate modified bundle manifest for operator hub.
	mkdir -p $(OPHUB_VERSION)
	cp -aT bundle $(OPHUB_VERSION)
	echo -e '\n  # Annotations for OperatorHub\n  com.redhat.openshift.versions: "v4.10"' >> $(OPHUB_VERSION)/metadata/annotations.yaml
