# Investigation: Issue #2522 - Panic "Diagnostic emitted without context"

## Issue
https://github.com/microsoft/typescript-go/issues/2522

**Title:** Reexporting @types/micromatch@4.0.10 from a .cjs file crashes

**Panic:** `panic: Diagnostic emitted without context` at `internal/transformers/declarations/transform.go:238`

## Reproduction

### Compiler Test (Reproduces the Panic)

File: `testdata/tests/cases/compiler/cjsModuleExportsReexportPanic.ts`

The test creates:
1. Two `@types` packages: `@types/dep` and `@types/extpkg`
2. `extpkg` imports and uses types from `dep` in its interface (the `dep()` method takes `dep.Options` parameter)
3. A `.cjs` file exports a variable typed as `typeof import("extpkg") | undefined` via `module.exports.extpkg = extpkg`

Run: `go test ./internal/testrunner/ -run 'TestLocal/cjsModuleExportsReexportPanic' -count=1 -v`

## Root Cause Analysis

### The Bug: Missing `getSymbolAccessibilityDiagnostic` in `transformCommonJSExport`

**Location:** `internal/transformers/declarations/transform.go`, lines 1098-1111

In `transformCommonJSExport`, when handling **non-default named exports** (the `else` branch at line 1098), the function does NOT set `tx.state.getSymbolAccessibilityDiagnostic` before calling `tx.ensureType(input, false)` at line 1101.

Compare with:
- The **default export** branch (line 1072-1080): DOES set `getSymbolAccessibilityDiagnostic`
- The **string-named export** branch (line 1112-1120): DOES set `getSymbolAccessibilityDiagnostic`

### Why `ensureType` Doesn't Fix It

`ensureType` (line 1192-1196) tries to set a diagnostic handler via `createGetSymbolAccessibilityDiagnosticForNode(node)`, but ONLY if `canProduceDiagnostics(node)` returns true. **`canProduceDiagnostics` does NOT include `KindCommonJSExport`** in its list (see `util.go:25-49`), so the diagnostic handler is never set.

### The Call Chain Leading to the Panic

1. `visitSourceFile` sets `getSymbolAccessibilityDiagnostic = throwDiagnostic` (line 250)
2. `visitDeclarationStatements` → `transformCommonJSExport` for `KindCommonJSExport` (line 1022)
3. In `transformCommonJSExport`, the non-default branch does NOT update `getSymbolAccessibilityDiagnostic`
4. `ensureType(CommonJSExport, false)` is called (line 1101)
5. `canProduceDiagnostics(CommonJSExport)` returns false, so the diagnostic handler stays as `throwDiagnostic`
6. Since `CommonJSExport.Type()` is nil (identifier initializer), `HasInferredType` is true
7. `CreateTypeOfDeclaration` → `SerializeTypeForDeclaration` with `tryReuse=true`
8. Pseudochecker returns `PseudoTypeKindInferred` for the identifier initializer
9. `pseudoTypeEquivalentToType` succeeds → `pseudoTypeToNode` is called
10. `pseudoTypeToNode` calls `serializeTypeForDeclaration(parent, nil, nil, false)` (tryReuse=false)
11. This calls `typeToTypeNode(t)` which expands the union type
12. For `typeof import("extpkg")`, the nodebuilder expands the type to include all members
13. The `dep()` method's parameter type `dep.Options` requires referencing the `dep` symbol
14. `lookupSymbolChain` → `TrackSymbol` → `IsSymbolAccessible` 
15. The `dep` type is from a different external module that can't be named from the CJS file
16. `handleSymbolAccessibilityError` is called with `CannotBeNamed` accessibility
17. `getSymbolAccessibilityDiagnostic` (still `throwDiagnostic`) is called → **PANIC!**

### Why `CannotBeNamed`?

In `isSymbolAccessibleWorker` (`symbolaccessibility.go:753`):
- `IsAnySymbolAccessible` returns nil (no accessible chain to the symbol from the CJS file)
- The symbol's external module container (`@types/dep`) differs from the enclosing module (CJS file)
- Result: `SymbolAccessibilityCannotBeNamed`

### Key Conditions for Reproduction

1. **Non-default CommonJS export:** `module.exports.NAME = value` where NAME ≠ "default"
2. **Identifier initializer:** The exported value is an identifier (not a literal or expression)
3. **Type references external module types:** The exported type, when fully expanded by the nodebuilder, references symbols from an external module (like `dep.Options` via `import dep = require("dep")`)
4. **Those symbols can't be named from the CJS file:** The external module's types aren't directly accessible from the CJS file's perspective

### The Fix

The fix should be straightforward: add `tx.state.getSymbolAccessibilityDiagnostic` to the non-default branch in `transformCommonJSExport`, similar to how the default and string-named branches handle it:

```go
} else {
    // export var name: Type
    tx.state.getSymbolAccessibilityDiagnostic = func(_ printer.SymbolAccessibilityResult) *SymbolAccessibilityDiagnostic {
        return &SymbolAccessibilityDiagnostic{
            diagnosticMessage: diagnostics.Exported_variable_0_has_or_is_using_name_1_from_external_module_2_but_cannot_be_named,
            errorNode:         input,
            typeName:          name,
        }
    }
    tx.tracker.PushErrorFallbackNode(input)
    type_ := tx.ensureType(input, false)
    // ...
}
```

Additionally, `canProduceDiagnostics` in `util.go` should likely include `ast.KindCommonJSExport` as a valid diagnostic-producing node kind, though this alone wouldn't fix the issue since `createGetSymbolAccessibilityDiagnosticForNode` also needs to handle `KindCommonJSExport`.
