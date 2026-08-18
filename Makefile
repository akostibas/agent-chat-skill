SKILL_DIR  ?= $(HOME)/.claude/skills/agent-chat
BINARY      = cmd/agent-chat/agent-chat
DOCKER_IMAGE ?= agent-chat-worker
CHANNEL     ?= worker-test

.PHONY: build install test unit clean fleet-test docker-build docker-run docker-test

build:
	go build -o $(BINARY) ./cmd/agent-chat/

install: build
	rm -rf "$(SKILL_DIR)"
	mkdir -p "$(SKILL_DIR)"
	cp -R skill/. "$(SKILL_DIR)/"
	cp $(BINARY) "$(SKILL_DIR)/agent-chat"
	chmod +x "$(SKILL_DIR)"/*.sh "$(SKILL_DIR)/agent-chat"
	@printf '%s\n' "$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" > "$(SKILL_DIR)/VERSION"
	"$(SKILL_DIR)/agent-chat" hook install
	@echo "Installed skill -> $(SKILL_DIR) ($$(cat "$(SKILL_DIR)/VERSION"))"

test: unit
	bin/smoke-test.sh
	bin/fleet-test.sh

unit:
	go test -race ./...

# Hermetic check of the fleet tooling (stubs `docker`; no image/containers).
fleet-test:
	bin/fleet-test.sh

clean:
	rm -f $(BINARY)

# --- containerized worker ---------------------------------------------------
docker-build:
	docker build -f docker/Dockerfile -t $(DOCKER_IMAGE) .

# Launch a worker on $(CHANNEL) against your real ~/.claude/agent-chat. Override
# the channel with CHANNEL=foo. Extracts subscription creds from the Keychain,
# so run it yourself (e.g. `! make docker-run`) so the token stays out of band.
docker-run: docker-build
	bin/docker-worker.sh $(CHANNEL) --image $(DOCKER_IMAGE)

# End-to-end round-trip against a throwaway channel; asserts the worker replies.
docker-test: docker-build
	bin/docker-worker-test.sh
