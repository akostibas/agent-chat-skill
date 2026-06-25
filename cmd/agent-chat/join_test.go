package main

import "testing"

func TestWithChannelRoot(t *testing.T) {
	base := `bash "/skills/stream.sh" "shannon-itest" "coordinator"`

	// Default global root (empty env): command is left untouched so existing
	// sessions keep working.
	if got := withChannelRoot(base, ""); got != base {
		t.Errorf("empty root should not modify command:\n got %q\nwant %q", got, base)
	}

	// Ephemeral container-fleet root: the printed command must carry the root,
	// quoted so paths with spaces survive copy-paste into Monitor. Regression
	// guard for issue #18.
	root := "/var/folders/ab/agent-chat-fleet-b30fd4ef"
	want := `AGENT_CHAT_ROOT="` + root + `" ` + base
	if got := withChannelRoot(base, root); got != want {
		t.Errorf("non-default root not propagated:\n got %q\nwant %q", got, want)
	}
}
