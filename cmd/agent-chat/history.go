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
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--since" || args[i] == "-since":
			if i+1 < len(args) {
				since = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "--since="):
			since = strings.TrimPrefix(args[i], "--since=")
		case !strings.HasPrefix(args[i], "-") && slug == "":
			slug = args[i]
		}
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "usage: agent-chat history <slug> [--since <iso8601>]")
		os.Exit(1)
	}
	validateIdent("slug", slug)
	return slug, since
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
