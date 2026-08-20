package main

import (
	"flag"
	"fmt"
	"os"
)

// Argument parsing for every subcommand. The shape is always the same — some
// positional idents plus a few long flags — and the flags may appear before or
// after the positionals (the shell shims put the slug first; agents don't
// reliably copy that order).
//
// flag.FlagSet alone can't do that: it stops at the first non-flag argument.
// parse() drives it in a loop instead, peeling one positional off each time it
// stops, which costs eight lines and replaces the six hand-rolled scanners this
// package used to carry.

// newFlagSet returns a FlagSet that reports errors as "usage: agent-chat <cmd>
// <form>" and exits 1, matching what the subcommands printed by hand.
func newFlagSet(cmd, form string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: agent-chat %s %s\n", cmd, form)
	}
	return fs
}

// parse parses args against fs and returns the positional arguments in order,
// accepting flags interleaved anywhere among them. Exits 1 on a bad flag.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(1)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// wantPositional enforces the exact positional count, printing the usage line
// and exiting when it doesn't match.
func wantPositional(fs *flag.FlagSet, pos []string, n int) {
	if len(pos) != n {
		fs.Usage()
		os.Exit(1)
	}
}

// parseSlugAs is the form for send/leave: both the slug and --as are required.
func parseSlugAs(cmd string, args []string) (slug, as string) {
	fs := newFlagSet(cmd, "<slug> --as <name>")
	asFlag := fs.String("as", "", "your name on the channel")
	pos := parse(fs, args)
	wantPositional(fs, pos, 1)
	if *asFlag == "" {
		fs.Usage()
		os.Exit(1)
	}
	validateIdent("slug", pos[0])
	validateIdent("name", *asFlag)
	return pos[0], *asFlag
}

// parseJoinArgs is join's form: slug required, --as optional. An empty as means
// "generate one"; cmdJoin fills it in.
func parseJoinArgs(args []string) (slug, as string) {
	fs := newFlagSet("join", "<slug> [--as <name>]")
	asFlag := fs.String("as", "", "your name on the channel (default: auto-generated)")
	pos := parse(fs, args)
	wantPositional(fs, pos, 1)
	validateIdent("slug", pos[0])
	if *asFlag != "" {
		validateIdent("name", *asFlag)
	}
	return pos[0], *asFlag
}
