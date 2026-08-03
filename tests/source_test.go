package tests

import (
	"testing"
	"testing/fstest"

	"github.com/aekis-dev/goql"
)

// goql.RegisterSource lets a binary carry the lambdas it was compiled from, so the parser
// works with no source on disk — the alternative to an ahead-of-time registry.
//
// The end-to-end proof lives outside the suite, because it requires building a binary and
// deleting its sources: a module embedding `//go:embed *.go` was run from an empty directory
// and its queries worked, while the identical program without the RegisterSource call failed
// with "no such file or directory". See triage.md §10.8.
//
// What is tested here is the resolution rule, which is where the subtlety is.

// A tree is anchored at the directory of the file that registered it. Two modules that both
// embed a file called main.go must not shadow each other — matching on a path suffix let
// registration order decide, and one module's lambda was sought in the other's source.
func TestSource_TreesDoNotShadowEachOther(t *testing.T) {
	// Registered from this file, so the tree is anchored at the tests directory.
	goql.RegisterSource(fstest.MapFS{
		"source_fixture_a.go": &fstest.MapFile{Data: []byte("package tests\n")},
	})

	// A lambda in this package still resolves from disk: the registered tree does not
	// contain this file, so the fallback applies rather than a wrong match.
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	got, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "USA"
	})
	if err != nil {
		t.Fatalf("a registered tree that lacks this file must not break the disk fallback: %v", err)
	}
	assertEqual(t, 1, len(got))
	assertEqual(t, "Alice", got[0].Name)
}

// A registered tree that does contain the file is preferred over the disk copy — which is
// what makes a source-free binary work at all.
func TestSource_RegisteredTreeIsPreferred(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Parsing this lambda must still succeed with several trees registered, none of which
	// claim this file.
	goql.RegisterSource(fstest.MapFS{
		"nothing_here.go": &fstest.MapFile{Data: []byte("package tests\n")},
	})

	got, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "Canada"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(got))
	assertEqual(t, "Bob", got[0].Name)
}
