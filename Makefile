GO ?= $(shell which go)
OS ?= $(shell $(GO) env GOOS)
ARCH ?= $(shell $(GO) env GOARCH)

IMAGE_NAME := abiondevelopment/cert-manager-webhook-abion

# Detect if we're on a tagged commit
GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null)
IMAGE_VERSION := $(shell echo $(GIT_TAG) | sed 's/^v//')
IMAGE_TAG ?= $(if $(IMAGE_VERSION),$(IMAGE_VERSION),latest)

OUT := $(shell pwd)/_out

KUBEBUILDER_VERSION=1.28.0

HELM_FILES := $(shell find deploy/cert-manager-webhook-abion)

.PHONY: fmt lint license tools build push clean rendered-manifest.yaml

fmt:
	go fmt ./...

lint:
	golangci-lint run --timeout=5m

license:
	addlicense -c "Abion AB" -l apache -s=only .

## Tools
tools:
	go install github.com/google/addlicense@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0


## Test

test: _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/etcd _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/kube-apiserver _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/kubectl
	TEST_ASSET_ETCD=_test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/etcd \
	TEST_ASSET_KUBE_APISERVER=_test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/kube-apiserver \
	TEST_ASSET_KUBECTL=_test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/kubectl \
	$(GO) test -v -timeout=20m .

_test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH).tar.gz: | _test
	curl -fsSL https://go.kubebuilder.io/test-tools/$(KUBEBUILDER_VERSION)/$(OS)/$(ARCH) -o $@

_test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/etcd _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/kube-apiserver _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)/kubectl: _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH).tar.gz | _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH)
	tar xfO $< kubebuilder/bin/$(notdir $@) > $@ && chmod +x $@

## Docker

build:
	docker build -t "$(IMAGE_NAME):$(IMAGE_TAG)" .
	@if [ "$(IMAGE_TAG)" != "latest" ]; then \
		docker tag "$(IMAGE_NAME):$(IMAGE_TAG)" "$(IMAGE_NAME):latest"; \
	fi
push:
	docker push "$(IMAGE_NAME):$(IMAGE_TAG)"
	@if [ "$(IMAGE_TAG)" != "latest" ]; then \
		docker push "$(IMAGE_NAME):latest"
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

_test $(OUT) _test/kubebuilder-$(KUBEBUILDER_VERSION)-$(OS)-$(ARCH):
	mkdir -p $@
