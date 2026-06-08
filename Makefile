SKILL_DIR ?= $(HOME)/.claude/skills/agent-chat

.PHONY: install test clean

install:
	rm -rf "$(SKILL_DIR)"
	mkdir -p "$(SKILL_DIR)"
	cp -R skill/. "$(SKILL_DIR)/"
	chmod +x "$(SKILL_DIR)"/*.sh
	@printf '%s\n' "$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" > "$(SKILL_DIR)/VERSION"
	@echo "Installed skill -> $(SKILL_DIR) ($$(cat "$(SKILL_DIR)/VERSION"))"

test:
	bin/smoke-test.sh

clean:
	@echo "nothing to clean"
