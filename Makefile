.DEFAULT_GOAL := help

SRC_DIR := src

.PHONY: generate manifests verify-generated fmt vet lint lint-fix test build docker-build clean help

generate manifests verify-generated fmt vet lint lint-fix test build docker-build clean:
	$(MAKE) -C $(SRC_DIR) $@

help:
	$(MAKE) -C $(SRC_DIR) help
