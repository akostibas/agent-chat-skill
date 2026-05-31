SKILL_DIR ?= $(HOME)/.claude/skills/agent-chat

.PHONY: install test clean

install:
	rm -rf "$(SKILL_DIR)"
	mkdir -p "$(SKILL_DIR)"
	cp -R skill/. "$(SKILL_DIR)/"
	chmod +x "$(SKILL_DIR)"/*.sh
	@echo "Installed skill -> $(SKILL_DIR)"

test:
	bin/smoke-test.sh

clean:
	@echo "nothing to clean"
