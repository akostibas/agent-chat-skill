package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/akostibas/agent-chat-skill/channel"
)

func cmdHistory(args []string) {
	slug, since := parseSlugSince(args)

	c := openChannel(slug)
	records, err := c.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	for _, r := range records {
		if since != "" && r.Ts < since {
			continue
		}
		fmt.Print(formatRecord(r))
	}
}

func parseSlugSince(args []string) (slug, since string) {
	fs := newFlagSet("history", "<slug> [--since <iso8601>]")
	sinceFlag := fs.String("since", "", "only records at or after this ISO8601 timestamp")
	pos := parse(fs, args)
	wantPositional(fs, pos, 1)
	validateIdent("slug", pos[0])
	return pos[0], *sinceFlag
}

// formatRecord reproduces the jq history output from the original history.sh:
//
//	━━━ <ts> <sender> (cwd=<cwd>[ branch=<branch>]) [<kind>] ━━━
//	<body>
func formatRecord(r channel.Record) string {
	var sb strings.Builder
	sb.WriteString("━━━ ")
	sb.WriteString(r.Ts)
	sb.WriteString(" ")
	sb.WriteString(r.Sender)
	sb.WriteString(" (cwd=")
	sb.WriteString(r.Cwd)
	if r.Branch != "" {
		sb.WriteString(" branch=")
		sb.WriteString(r.Branch)
	}
	sb.WriteString(") [")
	sb.WriteString(r.Kind)
	sb.WriteString("] ━━━\n")
	sb.WriteString(r.Body)
	sb.WriteString("\n\n")
	return sb.String()
}
