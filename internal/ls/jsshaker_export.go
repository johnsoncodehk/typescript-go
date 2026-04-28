package ls

import (
	"context"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/collections"
	"github.com/microsoft/typescript-go/internal/compiler"
)

// RefKind classifies a reference to a declaration.
type RefKind int

const (
	// RefKindDeclaration is a name node belonging to one of the symbol's own
	// declarations — e.g., the function's name, a paired get/set accessor's
	// name, or any binding the symbol is declared at.
	RefKindDeclaration RefKind = iota
	// RefKindCallSite is a reference in callee position: call/new/tagged
	// template / decorator / JSX opening element.
	RefKindCallSite
	// RefKindNonCallee is any other reference position. The function may be
	// invoked through it without a recoverable arg list — treat as escape.
	RefKindNonCallee
)

// ClassifiedRef pairs a reference node with its kind.
type ClassifiedRef struct {
	Node *ast.Node
	Kind RefKind
}

// ClassifyResult is what ClassifyReferences returns.
type ClassifyResult struct {
	Refs []ClassifiedRef
	// Resolved is true when the declaration was bound to a symbol that
	// could be queried for references. False means the declaration is an
	// anonymous expression with no analyzable binding (e.g., an arrow
	// passed inline as an argument).
	Resolved bool
}

// ClassifyReferences classifies every reference to `decl`. The declaration
// is resolved to a symbol via the standard call-hierarchy rule, with
// fallbacks for non-const var/let bindings, object literal property
// assignments, and binary-expression assignments — patterns common in
// bundled JavaScript that the standard rule rejects.
//
// References to any declaration of the resolved symbol are reported as
// RefKindDeclaration, so paired get/set accessors and overload signatures
// don't show up as escapes of each other.
func ClassifyReferences(
	ctx context.Context,
	program *compiler.Program,
	chk *checker.Checker,
	decl *ast.Node,
) ClassifyResult {
	sym, location := resolveDeclSymbol(chk, decl)
	if sym == nil {
		return ClassifyResult{}
	}

	sourceFiles := program.GetSourceFiles()
	set := collections.NewSetWithSizeHint[string](len(sourceFiles))
	for _, f := range sourceFiles {
		set.Add(f.FileName())
	}

	declNames := map[*ast.Node]struct{}{location: {}}
	declNodes := map[*ast.Node]struct{}{}
	for _, d := range sym.Declarations {
		declNodes[d] = struct{}{}
		if name := d.Name(); name != nil {
			declNames[name] = struct{}{}
		}
	}

	results := getReferencedSymbolsForSymbol(ctx, program, sym, location, sourceFiles, set, chk, refOptions{})

	var out []ClassifiedRef
	for _, sae := range results {
		for _, entry := range sae.references {
			if entry.kind != entryKindNode {
				continue
			}
			n := entry.node
			kind := RefKindNonCallee
			switch {
			case isDeclRef(n, declNames, declNodes):
				kind = RefKindDeclaration
			case ast.IsCallOrNewExpressionTarget(n, true, true),
				ast.IsTaggedTemplateTag(n, true, true),
				ast.IsDecoratorTarget(n, true, true),
				ast.IsJsxOpeningLikeElementTagName(n, true, true):
				kind = RefKindCallSite
			}
			out = append(out, ClassifiedRef{Node: n, Kind: kind})
		}
	}
	return ClassifyResult{Refs: out, Resolved: true}
}

// isDeclRef returns true when `n` is a declaration-name reference rather
// than a usage. Three cumulative checks:
//
//  1. Direct match: `n` is in the known declaration name set.
//  2. Symbol declaration: `n` is the name of one of `sym.Declarations`.
//  3. Structural: `n` is the name of a syntactic-declaration parent. This
//     catches inherited/overridden members in different classes — they have
//     different symbols but share names; find-references can return them
//     when the search broadens through inheritance.
func isDeclRef(n *ast.Node, declNames, declNodes map[*ast.Node]struct{}) bool {
	if _, ok := declNames[n]; ok {
		return true
	}
	p := n.Parent
	if p == nil {
		return false
	}
	if _, ok := declNodes[p]; ok && p.Name() == n {
		return true
	}
	if isDeclarationLikeKind(p.Kind) && p.Name() == n {
		return true
	}
	return false
}

// CallSiteOf walks up from a callsite reference (a callee-position node
// returned by ClassifyReferences) to the enclosing CallExpression or
// NewExpression. Returns nil if no enclosing call/new is found in the
// expected structural position.
func CallSiteOf(refNode *ast.Node) *ast.Node {
	cur := refNode
	for cur != nil && cur.Parent != nil {
		p := cur.Parent
		switch p.Kind {
		case ast.KindCallExpression:
			if p.AsCallExpression().Expression == cur {
				return p
			}
			return nil
		case ast.KindNewExpression:
			if p.AsNewExpression().Expression == cur {
				return p
			}
			return nil
		case ast.KindParenthesizedExpression,
			ast.KindPropertyAccessExpression,
			ast.KindElementAccessExpression,
			ast.KindNonNullExpression,
			ast.KindAsExpression,
			ast.KindSatisfiesExpression,
			ast.KindTypeAssertionExpression:
			cur = p
		default:
			return nil
		}
	}
	return nil
}

func isDeclarationLikeKind(k ast.Kind) bool {
	switch k {
	case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindArrowFunction,
		ast.KindMethodDeclaration, ast.KindMethodSignature,
		ast.KindGetAccessor, ast.KindSetAccessor,
		ast.KindClassDeclaration, ast.KindClassExpression,
		ast.KindVariableDeclaration, ast.KindParameter, ast.KindPropertyDeclaration,
		ast.KindPropertySignature,
		ast.KindBindingElement,
		ast.KindImportSpecifier, ast.KindImportClause, ast.KindNamespaceImport,
		ast.KindExportSpecifier, ast.KindNamespaceExport,
		ast.KindEnumDeclaration, ast.KindEnumMember,
		ast.KindInterfaceDeclaration, ast.KindTypeAliasDeclaration,
		ast.KindModuleDeclaration:
		return true
	}
	return false
}

// resolveDeclSymbol returns the symbol representing `decl`'s callable
// identity for find-references, plus a name node to use as the search
// location. Returns (nil, nil) if no analyzable binding can be found.
func resolveDeclSymbol(chk *checker.Checker, decl *ast.Node) (*ast.Symbol, *ast.Node) {
	// Standard path: named declarations + const-assigned expressions
	// (handled by getCallHierarchyDeclarationReferenceNode).
	if loc := getCallHierarchyDeclarationReferenceNode(decl); loc != nil {
		if sym := chk.GetSymbolAtLocation(loc); sym != nil {
			return sym, loc
		}
	}

	// Fallbacks for anonymous arrow/function/class expressions whose binding
	// the standard rule rejects (e.g., var/let, object property, exports.x = ).
	if !ast.IsArrowFunction(decl) && !ast.IsFunctionExpression(decl) && !ast.IsClassExpression(decl) {
		return nil, nil
	}
	if decl.Name() != nil {
		return nil, nil
	}
	parent := decl.Parent
	if parent == nil {
		return nil, nil
	}

	switch {
	case ast.IsVariableDeclaration(parent) && parent.Initializer() == decl:
		if name := parent.Name(); name != nil && ast.IsIdentifier(name) {
			if sym := chk.GetSymbolAtLocation(name); sym != nil {
				return sym, name
			}
		}
	case ast.IsPropertyAssignment(parent) && parent.Initializer() == decl:
		if name := parent.Name(); name != nil {
			if sym := chk.GetSymbolAtLocation(name); sym != nil {
				return sym, name
			}
		}
	case ast.IsBinaryExpression(parent):
		be := parent.AsBinaryExpression()
		if be.OperatorToken.Kind == ast.KindEqualsToken && be.Right == decl {
			if sym := chk.GetSymbolAtLocation(be.Left); sym != nil {
				return sym, be.Left
			}
		}
	}
	return nil, nil
}
