SKILL_DIR ?= $(HOME)/.claude/skills/agent-chat
BINARY     = cmd/agent-chat/agent-chat

.PHONY: build install test unit clean

build:
	go build -o $(BINARY) ./cmd/agent-chat/

install: build
	rm -rf "$(SKILL_DIR)"
	mkdir -p "$(SKILL_DIR)"
	cp -R skill/. "$(SKILL_DIR)/"
	cp $(BINARY) "$(SKILL_DIR)/agent-chat"
	chmod +x "$(SKILL_DIR)"/*.sh "$(SKILL_DIR)/agent-chat"
	@printf '%s\n' "$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" > "$(SKILL_DIR)/VERSION"
	@echo "Installed skill -> $(SKILL_DIR) ($$(cat "$(SKILL_DIR)/VERSION"))"

test: unit
	bin/smoke-test.sh

unit:
	go test -race ./...

clean:
	rm -f $(BINARY)
