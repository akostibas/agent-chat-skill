package main

import (
	"crypto/rand"
	"math/big"
)

// generateName returns a memorable "adjective-animal" identity drawn from the OS
// CSPRNG (crypto/rand reads /dev/urandom-class entropy).
//
// It exists because LLMs are poor entropy sources: when several sessions name
// themselves cold from near-identical context they converge on the same "clever"
// pick, which silently collapsed two workers into one identity (issue #16).
// Machine-owned entropy breaks that clustering while keeping names readable for
// the human watching the channel. Uniqueness is still *guaranteed* downstream by
// Channel.Join, which suffixes on the rare residual collision — this only makes
// collisions rare and the names pretty.
func generateName() string {
	return nameAdjectives[randIndex(len(nameAdjectives))] + "-" +
		nameAnimals[randIndex(len(nameAnimals))]
}

// randIndex returns a uniform random int in [0, n) from the CSPRNG. crypto/rand
// effectively never fails on supported platforms; if it somehow does we fall
// back to 0 rather than panic — a degenerate name is still better than a crash,
// and Join will disambiguate any resulting collision.
func randIndex(n int) int {
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(i.Int64())
}

// Curated wordlists. ~48×48 ≈ 2300 base combinations before Join's suffix
// backstop kicks in. Kept disjoint (no word appears in both lists) so a name
// always reads as adjective-then-animal. All entries match the identity regex
// ^[a-zA-Z0-9_-]{1,40}$.
var nameAdjectives = []string{
	"amber", "azure", "brisk", "clever", "cobalt", "copper", "coral", "crimson",
	"dapper", "dusky", "eager", "fabled", "fleet", "gilded", "hardy", "hazel",
	"indigo", "ivory", "jade", "jolly", "keen", "lively", "lucid", "mellow",
	"merry", "nimble", "noble", "olive", "opal", "plucky", "quiet", "rapid",
	"rusty", "sage", "scarlet", "sleek", "snowy", "spry", "sterling", "sunny",
	"swift", "teal", "tidy", "umber", "vivid", "witty", "zesty", "zippy",
}

var nameAnimals = []string{
	"addax", "badger", "bison", "bittern", "caracal", "cheetah", "civet", "dingo",
	"falcon", "ferret", "gecko", "gibbon", "heron", "ibex", "jackal", "jaguar",
	"kestrel", "koala", "lemur", "lynx", "macaw", "marten", "meerkat", "mongoose",
	"narwhal", "numbat", "ocelot", "osprey", "otter", "pangolin", "panther",
	"puffin", "quokka", "quoll", "raccoon", "raven", "salmon", "serval", "shrike",
	"tahr", "tamarin", "tapir", "urial", "vicuna", "walrus", "weasel", "wolverine",
	"wombat",
}
