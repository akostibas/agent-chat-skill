package main

import (
	"fmt"
	"os"
)

// cmdLeave is deliberate mid-session departure. Historically leaving meant
// killing your stream/wait process; a hook-subscribed peer (#59) has no
// process to kill, and its next tool call would self-heal a bare presence
// removal back into membership — so departure must also drop the session
// registration, or the hook resurrects the peer it just said goodbye for.
// This is the same teardown SessionEnd performs, on demand.
func cmdLeave(args []string) {
	slug, as := parseSlugAs("leave", args)
	c := openChannel(slug)
	if !c.Exists() {
		fmt.Fprintf(os.Stderr, "agent-chat: no such channel: %s\n", slug)
		os.Exit(1)
	}
	if err := c.Leave(as, "left channel"); err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: leave: %v\n", err)
		os.Exit(1)
	}
	_ = c.RemovePresence(as)
	_ = c.ClearReadOffset(as)
	deregisterSession(channelRoot(), slug)
	// The armed doorbell notices the missing presence and retires itself;
	// removing its lockfile keeps the hook from ever nagging about it.
	_ = os.Remove(doorbellPath(channelRoot(), slug, as))
	fmt.Printf("Left channel %q as %q. You will receive no further messages from it;\n", slug, as)
	fmt.Printf("re-join to subscribe again.\n")
}
