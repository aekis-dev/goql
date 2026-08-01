//go:build !prod

// Command goqlc regenerates this module's compiled lambda registry.
//
// Resolving a lambda's fields requires the models to be registered, which only happens
// when the packages declaring them are imported so their init() runs models.AddModel.
// A standalone tool cannot know those import paths, so the driver lives here, in the
// module being generated.
//
// To use goql in your own project, copy this file and replace the model import below
// with your own, then add a directive next to your queries:
//
//	//go:generate go run ./tools/goqlc .
package main

import (
	"log"
	"os"

	"github.com/aekis-dev/goql/generator"

	// Imported for its init(), which registers Customer, Order, Tag and Widget.
	_ "github.com/aekis-dev/goql/tests/models"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := generator.Run(dir); err != nil {
		log.Fatalf("goqlc: %v", err)
	}
}
