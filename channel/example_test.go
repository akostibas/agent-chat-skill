package channel_test

import (
	"context"
	"fmt"
	"os"

	"github.com/akostibas/agent-chat-skill/channel"
)

// Example shows the whole consumer flow: open a channel, register presence,
// post a message, and poll for new records with a cursor — the loop an
// external Go peer (e.g. a non-Claude-Code program sharing the channel dir)
// would run.
func Example() {
	root, _ := os.MkdirTemp("", "agent-chat-example")
	defer os.RemoveAll(root)

	c, err := channel.Open(root, "demo")
	if err != nil {
		panic(err)
	}
	ctx := context.Background()

	// Register as a peer so @mentions can address us and we appear in Members.
	_ = c.TouchPresence("worker")

	// Start listening from the current end, then post a message.
	cur, _ := c.End()
	_ = c.Append(ctx, channel.Record{Sender: "worker", Kind: "msg", Body: "ready"})

	// Poll: each call returns only what's new since the last cursor.
	recs, _, _ := c.ReadSince(ctx, cur)
	for _, r := range recs {
		fmt.Printf("%s: %s\n", r.Sender, r.Body)
	}
	// Output: worker: ready
}
