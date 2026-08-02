package goql

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

// LambdaKey is the registry key for a lambda compiled ahead of time: the base name of its
// source file and a line the Go runtime may report for it.
//
// It is shared by goqlc and the prod executor, the same way EnclosingFuncName and
// TopLevelFuncLits are, so ahead-of-time keying and runtime lookup cannot drift apart.
//
// Why position rather than the runtime's function name: the name is only stable for a
// literal written directly in a function. For a nested one the compiler rewrites it, and
// differently depending on inlining —
//
//	default build                        with -gcflags=all=-l
//	main.Outer.Outer.func2.func4         main.Outer.func2.1
//
// — so a nested lambda had no key at all, and the trailing number is a whole-function
// counter rather than an index within the parent. The reported line is identical in both,
// and across -N, -N -l and -race.
//
// Why the base name and not the path: -trimpath rewrites paths into module-relative form,
// so a path recorded at generation time would not match the one a trimmed binary reports.
// The base name survives it. goqlc rejects a build in which two lambdas would collide.
func LambdaKey(file string, line int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", filepath.Base(file), line)))
	return fmt.Sprintf("%x", sum[:8])
}
