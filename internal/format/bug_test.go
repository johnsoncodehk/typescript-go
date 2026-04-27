package format_test

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/format"
	"github.com/microsoft/typescript-go/internal/ls/lsutil"
	"github.com/microsoft/typescript-go/internal/parser"
	"gotest.tools/v3/assert"
)

func TestFormatWhitespaceBetweenComments(t *testing.T) {
	t.Parallel()
	// Regression test: whitespace-only line between two single-line comments
	// should not cause overlapping/duplicate edits that break formatting.
	text := "  const x = \"wont format\"\n//\n \n//\n"
	ctx := format.WithFormatCodeSettings(t.Context(), lsutil.FormatCodeSettings{
		EditorSettings: lsutil.EditorSettings{
			TabSize:                4,
			IndentSize:             4,
			NewLineCharacter:       "\n",
			ConvertTabsToSpaces:    core.TSTrue,
			IndentStyle:            lsutil.IndentStyleSmart,
			TrimTrailingWhitespace: core.TSTrue,
		},
	}, "\n")
	sourceFile := parser.ParseSourceFile(ast.SourceFileParseOptions{
		FileName: "/test.ts",
		Path:     "/test.ts",
	}, text, core.ScriptKindTS)
	edits := format.FormatDocument(ctx, sourceFile)

	// Verify no overlapping or duplicate edits
	for i := 1; i < len(edits); i++ {
		assert.Assert(t, edits[i].Pos() >= edits[i-1].End(),
			"Overlapping edits: edit %d [%d,%d) overlaps with edit %d [%d,%d)",
			i-1, edits[i-1].Pos(), edits[i-1].End(), i, edits[i].Pos(), edits[i].End())
	}

	// Verify edits can be applied without error
	result := applyBulkEdits(text, edits)
	assert.Assert(t, len(result) > 0)
}
