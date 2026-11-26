GO ?= $(shell which go)
OS ?= $(shell $(GO) env GOOS)
ARCH ?= $(shell $(GO) env GOARCH)

IMAGE_NAME := abiondevelopment/cert-manager-webhook-abion

# Detect if we're on a tagged commit
GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null)
IMAGE_VERSION := $(shell echo $(GIT_TAG) | sed 's/^v//')
IMAGE_TAG ?= $(if $(IMAGE_VERSION),$(IMAGE_VERSION),latest)

OUT := $(shell pwd)/_out

ENVTEST_VERSION=1.36.2
ENVTEST_DIR := _test/envtest-$(ENVTEST_VERSION)-$(OS)-$(ARCH)

HELM_FILES := $(shell find deploy/cert-manager-webhook-abion)

.PHONY: fmt lint license tools test test-conformance build push clean rendered-manifest.yaml

fmt:
	go fmt ./...

lint:
	golangci-lint run --timeout=5m

license:
	addlicense -c "Abion AB" -l apache -s=only .

## Tools
tools:
	go install github.com/google/addlicense@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0


## Test

test:
	$(GO) test -v -race ./...

test-conformance: $(ENVTEST_DIR)/etcd $(ENVTEST_DIR)/kube-apiserver $(ENVTEST_DIR)/kubectl
	TEST_ASSET_ETCD=$(ENVTEST_DIR)/etcd \
	TEST_ASSET_KUBE_APISERVER=$(ENVTEST_DIR)/kube-apiserver \
	TEST_ASSET_KUBECTL=$(ENVTEST_DIR)/kubectl \
	$(GO) test -v -tags conformance -timeout=20m .

$(ENVTEST_DIR).tar.gz: | _test
	curl -fsSL https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v$(ENVTEST_VERSION)/envtest-v$(ENVTEST_VERSION)-$(OS)-$(ARCH).tar.gz -o $@

$(ENVTEST_DIR)/etcd $(ENVTEST_DIR)/kube-apiserver $(ENVTEST_DIR)/kubectl: $(ENVTEST_DIR).tar.gz | $(ENVTEST_DIR)
	tar xfO $< controller-tools/envtest/$(notdir $@) > $@ && chmod +x $@

## Docker

build:
	docker build -t "$(IMAGE_NAME):$(IMAGE_TAG)" .
	@if [ "$(IMAGE_TAG)" != "latest" ]; then \
		docker tag "$(IMAGE_NAME):$(IMAGE_TAG)" "$(IMAGE_NAME):latest"; \
	fi
push:
	docker push "$(IMAGE_NAME):$(IMAGE_TAG)"
	@if [ "$(IMAGE_TAG)" != "latest" ]; then \
		docker push "$(IMAGE_NAME):latest"; \
	fi

clean:
	rm -r _test $(OUT)

rendered-manifest.yaml: $(OUT)/rendered-manifest.yaml

$(OUT)/rendered-manifest.yaml: $(HELM_FILES) | $(OUT)
	helm template \
	    --name cert-manager-webhook-abion \
            --set image.repository=$(IMAGE_NAME) \
            --set image.tag=$(IMAGE_TAG) \
            deploy/cert-manager-webhook-abion > $@

_test $(OUT) $(ENVTEST_DIR):
	mkdir -p $@
