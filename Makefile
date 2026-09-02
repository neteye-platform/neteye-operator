.DEFAULT_GOAL := help

SRC_DIR := src

.PHONY: generate manifests kustomize-manifests bundle bundle-validate bundle-build verify-generated fmt vet lint lint-fix test build docker-build clean help

generate manifests kustomize-manifests bundle bundle-validate bundle-build verify-generated fmt vet lint lint-fix test build docker-build clean:
	$(MAKE) -C $(SRC_DIR) $@

help:
	$(MAKE) -C $(SRC_DIR) help
