package format_test

import (
"testing"

"github.com/microsoft/typescript-go/internal/ast"
"github.com/microsoft/typescript-go/internal/core"
"github.com/microsoft/typescript-go/internal/format"
"github.com/microsoft/typescript-go/internal/ls/lsutil"
"github.com/microsoft/typescript-go/internal/parser"
)

func TestFormatCompare(t *testing.T) {
// Test with and without the whitespace between comments to see the diff
textA := "  const x = \"hello\"\n//\n\n//\n"      // no whitespace between comments (empty line)
textB := "  const x = \"hello\"\n//\n \n//\n"      // whitespace between comments (space on line 3)

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

t.Run("without whitespace (works)", func(t *testing.T) {
sourceFile := parser.ParseSourceFile(ast.SourceFileParseOptions{
FileName: "/test.ts",
Path:     "/test.ts",
}, textA, core.ScriptKindTS)
edits := format.FormatDocument(ctx, sourceFile)
t.Logf("Text: %q", textA)
for i, e := range edits {
t.Logf("Edit %d: pos=%d end=%d newText=%q [%q]", i, e.Pos(), e.End(), e.NewText, textA[e.Pos():e.End()])
}
result := applyBulkEdits(textA, edits)
t.Logf("Result: %q", result)
})

t.Run("with whitespace (breaks)", func(t *testing.T) {
sourceFile := parser.ParseSourceFile(ast.SourceFileParseOptions{
FileName: "/test.ts",
Path:     "/test.ts",
}, textB, core.ScriptKindTS)
edits := format.FormatDocument(ctx, sourceFile)
t.Logf("Text: %q", textB)
for i, e := range edits {
t.Logf("Edit %d: pos=%d end=%d newText=%q [%q]", i, e.Pos(), e.End(), e.NewText, textB[e.Pos():e.End()])
}
// Check for overlapping/duplicate edits
for i := 1; i < len(edits); i++ {
if edits[i].Pos() < edits[i-1].End() {
t.Errorf("Overlapping edits: edit %d [%d,%d) overlaps with edit %d [%d,%d)", i-1, edits[i-1].Pos(), edits[i-1].End(), i, edits[i].Pos(), edits[i].End())
}
}
})
}
