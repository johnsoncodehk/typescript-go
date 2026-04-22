package fourslash_test

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/fourslash"
	"github.com/microsoft/typescript-go/internal/testutil"
)

func TestQuickinfoVerbosityNamespaceClassHeritageCrash(t *testing.T) {
	t.Parallel()
	defer testutil.RecoverAndFail(t, "Panic on fourslash test")

	// Expanded namespace hover serializes Derived's generated class declaration,
	// including the class heritage clause for a constructor returning an intersection.
	const content = `
class Base {}

declare const Mixin: new () => Base & { mixed: string };

declare namespace NS/*1*/ {
    class Derived extends Mixin {}
}
`
	f, done := fourslash.NewFourslash(t, nil /*capabilities*/, content)
	defer done()

	f.VerifyBaselineHoverWithVerbosity(t, map[string][]int{"1": {0, 1}})
}
