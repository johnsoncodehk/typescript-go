package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/checker"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/ls"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

type escapeSample struct {
	funcName string
	line     int
	col      int
	snippet  string
}

type rankedDecl struct {
	decl      *ast.Node
	callCount int
	callsites []*ast.Node
}

type loaded struct {
	program *compiler.Program
	file    *ast.SourceFile
	chk     *checker.Checker
	done    func()
}

func main() {
	promote := flag.Bool("promote", false, "rewrite var/let to const + lift bare-var-then-assign to const decl")
	annotate := flag.Bool("annotate", false, "rewrite var/let to const + JSDoc-annotate bare-var-then-assign with type from RHS")
	flag.Parse()
	if *promote && *annotate {
		fmt.Fprintln(os.Stderr, "--promote and --annotate are mutually exclusive")
		os.Exit(2)
	}
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: spike [--promote|--annotate] <file>")
		os.Exit(2)
	}
	abs, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx := context.Background()

	t0 := time.Now()
	l := load(ctx, abs)
	progElapsed := time.Since(t0)

	displayPath := abs
	switch {
	case *promote:
		mutated, stats := detectAndPromote(l)
		total := stats.keywordSwaps + stats.lifts + stats.stmtDeletes
		fmt.Printf("Promote pass:          %d swaps, %d lifts, %d stmt deletes\n",
			stats.keywordSwaps, stats.lifts, stats.stmtDeletes)
		if total > 0 {
			displayPath = rebuildFromMutated(ctx, &l, abs, mutated, "promoted")
		}
	case *annotate:
		mutated, stats := detectAndAnnotate(l)
		total := stats.keywordSwaps + stats.annotations
		fmt.Printf("Annotate pass:         %d swaps, %d JSDoc annotations\n",
			stats.keywordSwaps, stats.annotations)
		if len(stats.samples) > 0 {
			fmt.Println("Sample annotations:")
			for i, s := range stats.samples {
				if i >= 8 {
					break
				}
				fmt.Printf("  %s @ line %d  /** @type {%s} */\n", s.name, s.line, truncateType(s.typeStr, 80))
			}
		}
		if total > 0 {
			displayPath = rebuildFromMutated(ctx, &l, abs, mutated, "annotated")
		}
	}
	defer l.done()

	runAnalysis(ctx, l, displayPath, progElapsed)
}

func load(ctx context.Context, path string) *loaded {
	cwd := filepath.Dir(path)
	fs := bundled.WrapFS(osvfs.FS())
	host := compiler.NewCompilerHost(cwd, fs, bundled.LibPath(), nil, nil)
	program := compiler.NewProgram(compiler.ProgramOptions{
		Config: &tsoptions.ParsedCommandLine{
			ParsedConfig: &core.ParsedOptions{
				FileNames:       []string{path},
				CompilerOptions: &core.CompilerOptions{AllowJs: core.TSTrue, CheckJs: core.TSTrue},
			},
		},
		Host: host,
	})
	file := program.GetSourceFile(path)
	if file == nil {
		fmt.Fprintln(os.Stderr, "could not find source file in program:", path)
		os.Exit(1)
	}
	chk, done := program.GetTypeChecker(ctx)
	return &loaded{program: program, file: file, chk: chk, done: done}
}

type rewrite struct {
	start, end int
	text       string
}

type promoteStats struct {
	keywordSwaps int // var/let → const (whole list)
	lifts        int // bare var + sole write → combined const
	stmtDeletes  int // var statements emptied by lifts
}

// detectAndPromote performs two source rewrites:
//
//  1. Wholesale keyword swap: a var/let VariableStatement whose every
//     binding has an initializer and is never written becomes `const`.
//
//  2. Lift-from-bare-declaration: a var/let binding with NO initializer
//     and exactly one assignment elsewhere in the same scope is removed
//     from its declaration list, and `const ` is prepended to the
//     assignment statement. This handles the bundler IIFE pattern
//     `var Node$1, Nodelist; ... Node$1 = class {...}; Nodelist = class
//     extends Node$1 {...};` where each binding's lone assignment is
//     effectively its initialization.
func detectAndPromote(l *loaded) (string, promoteStats) {
	stats := promoteStats{}
	writes := map[*ast.Symbol]int{}
	soleWrite := map[*ast.Symbol]*ast.Node{}

	markWrite := func(target *ast.Node) {
		if target == nil {
			return
		}
		var walk func(n *ast.Node)
		walk = func(n *ast.Node) {
			switch n.Kind {
			case ast.KindIdentifier:
				if sym := l.chk.GetSymbolAtLocation(n); sym != nil {
					writes[sym]++
					if writes[sym] == 1 {
						soleWrite[sym] = n
					} else {
						delete(soleWrite, sym)
					}
				}
			case ast.KindArrayLiteralExpression:
				for _, el := range n.AsArrayLiteralExpression().Elements.Nodes {
					walk(el)
				}
			case ast.KindObjectLiteralExpression:
				for _, prop := range n.AsObjectLiteralExpression().Properties.Nodes {
					if prop.Kind == ast.KindShorthandPropertyAssignment {
						walk(prop.Name())
					} else if prop.Kind == ast.KindPropertyAssignment {
						walk(prop.AsPropertyAssignment().Initializer)
					}
				}
			}
		}
		walk(target)
	}

	var visitWrites func(n *ast.Node) bool
	visitWrites = func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindBinaryExpression:
			be := n.AsBinaryExpression()
			if ast.IsAssignmentOperator(be.OperatorToken.Kind) {
				markWrite(be.Left)
			}
		case ast.KindPrefixUnaryExpression:
			pu := n.AsPrefixUnaryExpression()
			if pu.Operator == ast.KindPlusPlusToken || pu.Operator == ast.KindMinusMinusToken {
				markWrite(pu.Operand)
			}
		case ast.KindPostfixUnaryExpression:
			pu := n.AsPostfixUnaryExpression()
			if pu.Operator == ast.KindPlusPlusToken || pu.Operator == ast.KindMinusMinusToken {
				markWrite(pu.Operand)
			}
		case ast.KindForInStatement, ast.KindForOfStatement:
			init := n.Initializer()
			if init != nil && init.Kind != ast.KindVariableDeclarationList {
				markWrite(init)
			}
		}
		return n.ForEachChild(visitWrites)
	}
	visitWrites(l.file.AsNode())

	var rewrites []rewrite
	text := l.file.Text()

	var visitDecls func(n *ast.Node) bool
	visitDecls = func(n *ast.Node) bool {
		if n.Kind == ast.KindVariableStatement {
			vs := n
			list := vs.AsVariableStatement().DeclarationList
			flags := list.Flags & (ast.NodeFlagsLet | ast.NodeFlagsConst | ast.NodeFlagsUsing | ast.NodeFlagsAwaitUsing)
			isConstAlready := flags&ast.NodeFlagsConst != 0 || flags&ast.NodeFlagsUsing != 0 || flags&ast.NodeFlagsAwaitUsing != 0
			if !isConstAlready {
				rewrites = append(rewrites, planRewrites(vs, list, text, writes, soleWrite, l.chk, &stats)...)
			}
		}
		return n.ForEachChild(visitDecls)
	}
	visitDecls(l.file.AsNode())

	if len(rewrites) == 0 {
		return text, stats
	}
	sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].start < rewrites[j].start })

	var sb strings.Builder
	sb.Grow(len(text) + 16*len(rewrites))
	prev := 0
	for _, r := range rewrites {
		if r.start < prev {
			continue // overlapping; keep first
		}
		sb.WriteString(text[prev:r.start])
		sb.WriteString(r.text)
		prev = r.end
	}
	sb.WriteString(text[prev:])
	return sb.String(), stats
}

// planRewrites figures out what to do with a single var/let VariableStatement.
// Returns rewrites for: wholesale keyword swap, individual binding lifts,
// or full statement deletion when every binding is lifted out.
func planRewrites(
	vs, list *ast.Node,
	text string,
	writes map[*ast.Symbol]int,
	soleWrite map[*ast.Symbol]*ast.Node,
	chk *checker.Checker,
	stats *promoteStats,
) []rewrite {
	bindings := list.AsVariableDeclarationList().Declarations.Nodes

	// Per-binding classification.
	type kind int
	const (
		kindKeep kind = iota
		kindPromote
		kindLift
	)
	type bindingPlan struct {
		kind        kind
		liftWriteEs *ast.Node // ExpressionStatement of the sole write (only for kindLift)
	}
	plans := make([]bindingPlan, len(bindings))
	allPromote := len(bindings) > 0
	allLift := len(bindings) > 0
	anyLift := false

	for i, b := range bindings {
		name := b.Name()
		init := b.Initializer()

		if init != nil {
			// Has init: candidate for wholesale promote if no writes.
			if name.Kind == ast.KindIdentifier {
				sym := chk.GetSymbolAtLocation(name)
				if sym != nil && writes[sym] == 0 {
					plans[i] = bindingPlan{kind: kindPromote}
					allLift = false
					continue
				}
			} else if isBindingPatternNoWrites(name, chk, writes) {
				plans[i] = bindingPlan{kind: kindPromote}
				allLift = false
				continue
			}
			plans[i] = bindingPlan{kind: kindKeep}
			allPromote = false
			allLift = false
			continue
		}

		// No init: candidate for lift if exactly 1 write at statement level.
		if name.Kind != ast.KindIdentifier {
			plans[i] = bindingPlan{kind: kindKeep}
			allPromote = false
			allLift = false
			continue
		}
		sym := chk.GetSymbolAtLocation(name)
		if sym == nil || writes[sym] != 1 {
			plans[i] = bindingPlan{kind: kindKeep}
			allPromote = false
			allLift = false
			continue
		}
		w := soleWrite[sym]
		es := liftableAssignment(w, enclosingScope(name))
		if es == nil {
			plans[i] = bindingPlan{kind: kindKeep}
			allPromote = false
			allLift = false
			continue
		}
		plans[i] = bindingPlan{kind: kindLift, liftWriteEs: es}
		allPromote = false
		anyLift = true
	}

	var out []rewrite

	// All bindings have init + no writes → swap keyword to const.
	if allPromote {
		kwStart := scanner.SkipTrivia(text, list.Pos())
		out = append(out, rewrite{kwStart, kwStart + 3, "const"})
		stats.keywordSwaps++
		return out
	}

	if !anyLift {
		return nil
	}

	// Some bindings are lift-eligible.
	if allLift {
		// Erase the whole statement; each lift-write gets `const ` prepended.
		out = append(out, rewrite{vs.Pos(), vs.End(), strings.Repeat(" ", 0)})
		// Also need to erase any original whitespace including the trailing newline?
		// Pos→End covers the statement node (excluding leading trivia). Good enough.
		stats.stmtDeletes++
	} else {
		// Erase individual lift-bindings from the declaration list.
		for i, b := range bindings {
			if plans[i].kind != kindLift {
				continue
			}
			var start, end int
			if i == 0 {
				start = b.Pos()
				end = bindings[1].Pos()
			} else {
				start = bindings[i-1].End()
				end = b.End()
			}
			out = append(out, rewrite{start, end, ""})
		}
	}

	// Prepend `const ` to each lift-write's ExpressionStatement.
	for i, p := range plans {
		if p.kind != kindLift {
			continue
		}
		_ = bindings[i]
		es := p.liftWriteEs
		insertAt := scanner.SkipTrivia(text, es.Pos())
		out = append(out, rewrite{insertAt, insertAt, "const "})
		stats.lifts++
	}

	return out
}

// isBindingPatternNoWrites returns true for object/array binding patterns
// where every introduced symbol has zero writes.
func isBindingPatternNoWrites(name *ast.Node, chk *checker.Checker, writes map[*ast.Symbol]int) bool {
	if name == nil {
		return false
	}
	switch name.Kind {
	case ast.KindIdentifier:
		sym := chk.GetSymbolAtLocation(name)
		return sym != nil && writes[sym] == 0
	case ast.KindObjectBindingPattern, ast.KindArrayBindingPattern:
		for _, el := range name.AsBindingPattern().Elements.Nodes {
			if el.Kind == ast.KindOmittedExpression {
				continue
			}
			if !isBindingPatternNoWrites(el.AsBindingElement().Name(), chk, writes) {
				return false
			}
		}
		return true
	}
	return false
}

// liftableAssignment returns the ExpressionStatement of a sole write
// `X = expr;` only if it's a statement-position assignment whose enclosing
// function/source scope matches `declScope`. Returns nil for `if (X = ...)`,
// `(X = ...) +`, destructuring assignments, or assignments in a nested
// function (which would change the binding's effective scope after lift).
func liftableAssignment(writeIdent *ast.Node, declScope *ast.Node) *ast.Node {
	if writeIdent == nil || writeIdent.Parent == nil {
		return nil
	}
	be := writeIdent.Parent
	if be.Kind != ast.KindBinaryExpression {
		return nil
	}
	bex := be.AsBinaryExpression()
	if bex.OperatorToken.Kind != ast.KindEqualsToken {
		return nil
	}
	if bex.Left != writeIdent {
		return nil
	}
	if be.Parent == nil || be.Parent.Kind != ast.KindExpressionStatement {
		return nil
	}
	if enclosingScope(be.Parent) != declScope {
		return nil
	}
	return be.Parent
}

// enclosingScope returns the nearest function-like or source-file ancestor.
// This matches `var` hoisting boundaries.
func enclosingScope(n *ast.Node) *ast.Node {
	for cur := n.Parent; cur != nil; cur = cur.Parent {
		switch cur.Kind {
		case ast.KindFunctionDeclaration, ast.KindFunctionExpression,
			ast.KindArrowFunction, ast.KindMethodDeclaration,
			ast.KindGetAccessor, ast.KindSetAccessor,
			ast.KindConstructor, ast.KindClassStaticBlockDeclaration,
			ast.KindSourceFile:
			return cur
		}
	}
	return nil
}

func rebuildFromMutated(ctx context.Context, l **loaded, origPath, mutated, label string) string {
	tempPath, err := writeTempCopy(origPath, mutated)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempfile:", err)
		os.Exit(1)
	}
	fmt.Printf("%s file:           %s\n", strings.Title(label), tempPath)
	(*l).done()
	t0 := time.Now()
	*l = load(ctx, tempPath)
	fmt.Printf("Re-build program:      %v  (post-%s)\n", time.Since(t0), label)
	return tempPath
}

type annotateStats struct {
	keywordSwaps int
	annotations  int
	samples      []annotateSample
}

type annotateSample struct {
	name    string
	line    int
	typeStr string
}

// detectAndAnnotate is the JSDoc counterpart to detectAndPromote. It still
// performs the wholesale keyword swap (var/let → const for fully-promotable
// lists) but instead of lifting bare-var-then-assign, it injects a
// `/** @type {...} */` annotation derived from the assignment's RHS type.
//
// For multi-binding declarations (`var A, B, C;`) where some bindings need
// annotation, the statement is split into per-binding statements so each
// can carry its own JSDoc comment.
func detectAndAnnotate(l *loaded) (string, annotateStats) {
	stats := annotateStats{}
	writes, soleWrite := collectWrites(l)
	text := l.file.Text()

	var rewrites []rewrite

	var visit func(n *ast.Node) bool
	visit = func(n *ast.Node) bool {
		if n.Kind == ast.KindVariableStatement {
			rewrites = append(rewrites, planAnnotateRewrites(n, text, writes, soleWrite, l.chk, &stats)...)
		}
		return n.ForEachChild(visit)
	}
	visit(l.file.AsNode())

	return applyRewrites(text, rewrites), stats
}

func planAnnotateRewrites(
	vs *ast.Node,
	text string,
	writes map[*ast.Symbol]int,
	soleWrite map[*ast.Symbol]*ast.Node,
	chk *checker.Checker,
	stats *annotateStats,
) []rewrite {
	list := vs.AsVariableStatement().DeclarationList
	flags := list.Flags & (ast.NodeFlagsLet | ast.NodeFlagsConst | ast.NodeFlagsUsing | ast.NodeFlagsAwaitUsing)
	if flags&ast.NodeFlagsConst != 0 || flags&ast.NodeFlagsUsing != 0 || flags&ast.NodeFlagsAwaitUsing != 0 {
		return nil
	}

	bindings := list.AsVariableDeclarationList().Declarations.Nodes
	type action struct {
		kind    int // 0 keep, 1 swap, 2 annotate
		typeStr string
	}
	actions := make([]action, len(bindings))
	canSwapAll := len(bindings) > 0
	needSplit := false

	for i, b := range bindings {
		name := b.Name()
		init := b.Initializer()
		if init != nil {
			if isNameNoWrites(name, chk, writes) {
				actions[i] = action{kind: 1}
				continue
			}
			actions[i] = action{kind: 0}
			canSwapAll = false
			continue
		}
		// No init: candidate for annotation.
		if name.Kind == ast.KindIdentifier {
			sym := chk.GetSymbolAtLocation(name)
			if sym != nil && writes[sym] == 1 {
				w := soleWrite[sym]
				if rhs := assignmentRHS(w); rhs != nil {
					t := chk.GetTypeAtLocation(rhs)
					flags := checker.TypeFormatFlagsWriteClassExpressionAsTypeLiteral |
						checker.TypeFormatFlagsNoTruncation |
						checker.TypeFormatFlagsUseStructuralFallback
					ts := sanitizeTypeForJSDoc(chk.TypeToStringEx(t, nil, flags, nil))
					if ts != "" && ts != "any" {
						actions[i] = action{kind: 2, typeStr: ts}
						needSplit = true
						canSwapAll = false
						continue
					}
				}
			}
		}
		actions[i] = action{kind: 0}
		canSwapAll = false
	}

	if canSwapAll {
		kwStart := scanner.SkipTrivia(text, list.Pos())
		stats.keywordSwaps++
		return []rewrite{{kwStart, kwStart + 3, "const"}}
	}
	if !needSplit {
		return nil
	}

	origKw := "var"
	if flags&ast.NodeFlagsLet != 0 {
		origKw = "let"
	}

	var sb strings.Builder
	sb.WriteString("\n")
	for i, b := range bindings {
		a := actions[i]
		if i > 0 {
			sb.WriteString("\n")
		}
		if a.kind == 2 {
			sb.WriteString("/** @type {")
			sb.WriteString(a.typeStr)
			sb.WriteString("} */ ")
			stats.annotations++
			if len(stats.samples) < 8 {
				bStart := scanner.SkipTrivia(text, b.Pos())
				line, _ := scanner.GetECMALineAndByteOffsetOfPosition(asSourceFileLikeFromNode(vs), bStart)
				name := "<unknown>"
				if id := b.Name(); id != nil && id.Kind == ast.KindIdentifier {
					name = id.AsIdentifier().Text
				}
				stats.samples = append(stats.samples, annotateSample{name, line + 1, a.typeStr})
			}
		}
		switch a.kind {
		case 1:
			sb.WriteString("const")
		default:
			sb.WriteString(origKw)
		}
		sb.WriteString(" ")
		bStart := scanner.SkipTrivia(text, b.Pos())
		sb.WriteString(text[bStart:b.End()])
		sb.WriteString(";")
	}

	return []rewrite{{vs.Pos(), vs.End(), sb.String()}}
}

func assignmentRHS(writeIdent *ast.Node) *ast.Node {
	if writeIdent == nil || writeIdent.Parent == nil {
		return nil
	}
	be := writeIdent.Parent
	if be.Kind != ast.KindBinaryExpression {
		return nil
	}
	bex := be.AsBinaryExpression()
	if bex.OperatorToken.Kind != ast.KindEqualsToken {
		return nil
	}
	if bex.Left != writeIdent {
		return nil
	}
	return bex.Right
}

func isNameNoWrites(name *ast.Node, chk *checker.Checker, writes map[*ast.Symbol]int) bool {
	if name == nil {
		return false
	}
	switch name.Kind {
	case ast.KindIdentifier:
		sym := chk.GetSymbolAtLocation(name)
		return sym != nil && writes[sym] == 0
	case ast.KindObjectBindingPattern, ast.KindArrayBindingPattern:
		for _, el := range name.AsBindingPattern().Elements.Nodes {
			if el.Kind == ast.KindOmittedExpression {
				continue
			}
			if !isNameNoWrites(el.AsBindingElement().Name(), chk, writes) {
				return false
			}
		}
		return true
	}
	return false
}

func sanitizeTypeForJSDoc(s string) string {
	s = strings.ReplaceAll(s, "*/", "* /")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

func asSourceFileLikeFromNode(n *ast.Node) ast.SourceFileLike {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Kind == ast.KindSourceFile {
			return cur.AsSourceFile()
		}
	}
	return nil
}

func collectWrites(l *loaded) (map[*ast.Symbol]int, map[*ast.Symbol]*ast.Node) {
	writes := map[*ast.Symbol]int{}
	soleWrite := map[*ast.Symbol]*ast.Node{}

	markWrite := func(target *ast.Node) {
		if target == nil {
			return
		}
		var walk func(n *ast.Node)
		walk = func(n *ast.Node) {
			switch n.Kind {
			case ast.KindIdentifier:
				if sym := l.chk.GetSymbolAtLocation(n); sym != nil {
					writes[sym]++
					if writes[sym] == 1 {
						soleWrite[sym] = n
					} else {
						delete(soleWrite, sym)
					}
				}
			case ast.KindArrayLiteralExpression:
				for _, el := range n.AsArrayLiteralExpression().Elements.Nodes {
					walk(el)
				}
			case ast.KindObjectLiteralExpression:
				for _, prop := range n.AsObjectLiteralExpression().Properties.Nodes {
					if prop.Kind == ast.KindShorthandPropertyAssignment {
						walk(prop.Name())
					} else if prop.Kind == ast.KindPropertyAssignment {
						walk(prop.AsPropertyAssignment().Initializer)
					}
				}
			}
		}
		walk(target)
	}

	var visit func(n *ast.Node) bool
	visit = func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindBinaryExpression:
			be := n.AsBinaryExpression()
			if ast.IsAssignmentOperator(be.OperatorToken.Kind) {
				markWrite(be.Left)
			}
		case ast.KindPrefixUnaryExpression:
			pu := n.AsPrefixUnaryExpression()
			if pu.Operator == ast.KindPlusPlusToken || pu.Operator == ast.KindMinusMinusToken {
				markWrite(pu.Operand)
			}
		case ast.KindPostfixUnaryExpression:
			pu := n.AsPostfixUnaryExpression()
			if pu.Operator == ast.KindPlusPlusToken || pu.Operator == ast.KindMinusMinusToken {
				markWrite(pu.Operand)
			}
		case ast.KindForInStatement, ast.KindForOfStatement:
			init := n.Initializer()
			if init != nil && init.Kind != ast.KindVariableDeclarationList {
				markWrite(init)
			}
		}
		return n.ForEachChild(visit)
	}
	visit(l.file.AsNode())

	return writes, soleWrite
}

func applyRewrites(text string, rewrites []rewrite) string {
	if len(rewrites) == 0 {
		return text
	}
	sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].start < rewrites[j].start })
	var sb strings.Builder
	sb.Grow(len(text) + 16*len(rewrites))
	prev := 0
	for _, r := range rewrites {
		if r.start < prev {
			continue
		}
		sb.WriteString(text[prev:r.start])
		sb.WriteString(r.text)
		prev = r.end
	}
	sb.WriteString(text[prev:])
	return sb.String()
}

func writeTempCopy(origPath, content string) (string, error) {
	dir := os.TempDir()
	base := filepath.Base(origPath)
	tempPath := filepath.Join(dir, "spike-promoted-"+base)
	if err := os.WriteFile(tempPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return tempPath, nil
}

func runAnalysis(ctx context.Context, l *loaded, origPath string, progElapsed time.Duration) {
	t1 := time.Now()
	chkElapsed := time.Since(t1) // already loaded; keep label

	var decls []*ast.Node
	var visit func(n *ast.Node) bool
	visit = func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindArrowFunction,
			ast.KindMethodDeclaration, ast.KindGetAccessor, ast.KindSetAccessor:
			decls = append(decls, n)
		}
		return n.ForEachChild(visit)
	}
	visit(l.file.AsNode())

	var (
		safe, escaped, unused, noSym int
		totalCalls                   int
		totalEscapes                 int
		escapeKinds                  = map[ast.Kind]int{}
		escapeSamples                = map[ast.Kind][]escapeSample{}
		safeRanked                   []rankedDecl
	)

	t2 := time.Now()
	for _, d := range decls {
		result := ls.ClassifyReferences(ctx, l.program, l.chk, d)
		if !result.Resolved {
			noSym++
			continue
		}
		var calls, escapes int
		var callsiteRefs []*ast.Node
		for _, r := range result.Refs {
			switch r.Kind {
			case ls.RefKindCallSite:
				calls++
				callsiteRefs = append(callsiteRefs, r.Node)
			case ls.RefKindNonCallee:
				escapes++
				parent := r.Node.Parent
				if parent != nil {
					kind := parent.Kind
					escapeKinds[kind]++
					if len(escapeSamples[kind]) < 5 {
						line, col := lineCol(l.file, r.Node.Pos())
						escapeSamples[kind] = append(escapeSamples[kind], escapeSample{
							funcName: declName(d),
							line:     line,
							col:      col,
							snippet:  snippet(l.file, parent, 70),
						})
					}
				}
			}
		}
		totalCalls += calls
		totalEscapes += escapes
		switch {
		case calls == 0 && escapes == 0:
			unused++
		case escapes == 0:
			safe++
			safeRanked = append(safeRanked, rankedDecl{d, calls, callsiteRefs})
		default:
			escaped++
		}
	}
	classifyElapsed := time.Since(t2)

	fmt.Printf("File:                  %s (%d bytes)\n", origPath, len(l.file.Text()))
	fmt.Printf("Build program:         %v\n", progElapsed)
	fmt.Printf("Get type checker:      %v\n", chkElapsed)
	fmt.Printf("Classify all decls:    %v  (avg %v)\n", classifyElapsed,
		classifyElapsed/time.Duration(max(len(decls), 1)))
	if diags := l.file.Diagnostics(); len(diags) > 0 {
		fmt.Printf("Diagnostics:           %d\n", len(diags))
	}
	fmt.Printf("Function-like decls:   %d\n", len(decls))
	fmt.Printf("  Safe (callee only):  %d  (%.1f%%)\n", safe, pct(safe, len(decls)))
	fmt.Printf("  Escaped:             %d  (%.1f%%)\n", escaped, pct(escaped, len(decls)))
	fmt.Printf("  Unused (no refs):    %d  (%.1f%%)\n", unused, pct(unused, len(decls)))
	fmt.Printf("  Unresolved binding:  %d  (%.1f%%)\n", noSym, pct(noSym, len(decls)))
	fmt.Printf("Total call-site refs:  %d\n", totalCalls)
	fmt.Printf("Total escape refs:     %d\n", totalEscapes)
	fmt.Println()

	type kindCount struct {
		kind  ast.Kind
		count int
	}
	hist := make([]kindCount, 0, len(escapeKinds))
	for k, c := range escapeKinds {
		hist = append(hist, kindCount{k, c})
	}
	sort.Slice(hist, func(i, j int) bool { return hist[i].count > hist[j].count })

	fmt.Println("Escape contexts (parent of non-callee reference):")
	for i, kc := range hist {
		if i >= 15 {
			break
		}
		fmt.Printf("  %-32s %6d  (%.1f%%)\n", kc.kind.String(), kc.count, pct(kc.count, totalEscapes))
	}
	fmt.Println()

	sort.Slice(safeRanked, func(i, j int) bool {
		return safeRanked[i].callCount > safeRanked[j].callCount
	})
	limit := 20
	if len(safeRanked) < limit {
		limit = len(safeRanked)
	}
	fmt.Printf("Top %d safe-to-specialize by callers:\n", limit)
	for _, rd := range safeRanked[:limit] {
		ll, c := lineCol(l.file, rd.decl.Pos())
		fmt.Printf("  %-30s @ %d:%d  callers=%d\n", declName(rd.decl), ll, c, rd.callCount)
	}
	fmt.Println()

	const opportunityScanLimit = 100
	scanLimit := opportunityScanLimit
	if len(safeRanked) < scanLimit {
		scanLimit = len(safeRanked)
	}
	const detailLimit = 10
	detail := detailLimit
	if scanLimit < detail {
		detail = scanLimit
	}

	var allOpps []paramOpportunity
	fmt.Printf("Argument type analysis (top %d safe by callers; scanning top %d for opportunities):\n",
		detail, scanLimit)
	for i, rd := range safeRanked[:scanLimit] {
		opps := analyzeArgTypes(rd, l.chk, l.file, i < detail)
		allOpps = append(allOpps, opps...)
	}

	sort.Slice(allOpps, func(i, j int) bool { return allOpps[i].score > allOpps[j].score })
	fmt.Println()
	fmt.Println("Specialization opportunities (highest score first):")
	oppLimit := 25
	if len(allOpps) < oppLimit {
		oppLimit = len(allOpps)
	}
	for _, o := range allOpps[:oppLimit] {
		fmt.Printf("  %-28s @%-6d param %d: %-12s %3d/%-3d narrow, top: %dx %s  (score=%.0f)\n",
			truncateType(o.funcName, 28), o.funcLine, o.paramIdx,
			o.classification, o.narrowCallers, o.totalCallers,
			o.dominantCount, truncateType(o.dominantType, 30), o.score)
	}
}

type paramOpportunity struct {
	funcName       string
	funcLine       int
	paramIdx       int
	classification string
	totalCallers   int
	narrowCallers  int
	dominantType   string
	dominantCount  int
	score          float64
}

// analyzeArgTypes prints per-param type histograms for `rd` (when verbose
// is true) and returns specialization opportunities for each parameter
// regardless of verbosity.
func analyzeArgTypes(rd rankedDecl, chk *checker.Checker, file *ast.SourceFile, verbose bool) []paramOpportunity {
	name := declName(rd.decl)
	line, _ := lineCol(file, rd.decl.Pos())

	perArg := []map[string]int{}
	maxArity := 0
	resolved := 0
	for _, ref := range rd.callsites {
		call := ls.CallSiteOf(ref)
		if call == nil {
			continue
		}
		resolved++
		var args []*ast.Node
		switch call.Kind {
		case ast.KindCallExpression:
			args = call.AsCallExpression().Arguments.Nodes
		case ast.KindNewExpression:
			if a := call.AsNewExpression().Arguments; a != nil {
				args = a.Nodes
			}
		}
		if len(args) > maxArity {
			maxArity = len(args)
		}
		for i, arg := range args {
			for len(perArg) <= i {
				perArg = append(perArg, map[string]int{})
			}
			t := chk.GetTypeAtLocation(arg)
			perArg[i][chk.TypeToString(t)]++
		}
	}

	if verbose {
		fmt.Printf("\n  %s @ line %d  (callers=%d, resolved=%d, max arity=%d)\n",
			name, line, rd.callCount, resolved, maxArity)
	}

	var opps []paramOpportunity
	for i, hist := range perArg {
		opp := classifyParam(hist, resolved)
		opp.funcName = name
		opp.funcLine = line
		opp.paramIdx = i
		opps = append(opps, opp)

		if verbose {
			type tc struct {
				typ   string
				count int
			}
			ranked := make([]tc, 0, len(hist))
			for k, v := range hist {
				ranked = append(ranked, tc{k, v})
			}
			sort.Slice(ranked, func(a, b int) bool { return ranked[a].count > ranked[b].count })

			fmt.Printf("    param %d [%s]: ", i, opp.classification)
			for j, t := range ranked {
				if j >= 4 {
					fmt.Printf(", ... +%d more", len(ranked)-4)
					break
				}
				if j > 0 {
					fmt.Print(", ")
				}
				fmt.Printf("%dx %s", t.count, truncateType(t.typ, 40))
			}
			fmt.Println()
		}
	}
	return opps
}

// classifyParam buckets a parameter slot by its caller-type histogram.
// Two axes matter: narrowFrac (how many callers carry type info) and the
// distribution among narrow callers.
//
// Classifications, ordered by specialization value:
//
//   - monomorphic: ≥95% of all callers pass the same narrow type. One
//     fast path covers everyone.
//   - monomorphic-narrow: ≥95% of NARROW callers agree, but narrowFrac is
//     under 95% (some callers are `any`). Specialize the narrow path,
//     keep a generic fallback for the rest.
//   - bimodal: top two narrow types cover ≥85% of NARROW callers, each
//     ≥10%. Specialize with one if/else.
//   - polymorphic: 3-5 distinct narrow types dominate. Switch dispatch.
//   - spread: many narrow types, no clear majority. Low ROI.
//   - generic: no narrow callers at all.
//
// Score = approximate number of callers that benefit from specialization.
func classifyParam(hist map[string]int, totalCallers int) paramOpportunity {
	if totalCallers == 0 {
		return paramOpportunity{classification: "no-callers"}
	}
	type tc struct {
		typ   string
		count int
	}
	ranked := make([]tc, 0, len(hist))
	narrow := 0
	for k, v := range hist {
		ranked = append(ranked, tc{k, v})
		if !isWideType(k) {
			narrow += v
		}
	}
	sort.Slice(ranked, func(a, b int) bool { return ranked[a].count > ranked[b].count })

	var narrowSorted []tc
	for _, r := range ranked {
		if !isWideType(r.typ) {
			narrowSorted = append(narrowSorted, r)
		}
	}

	if len(narrowSorted) == 0 {
		return paramOpportunity{
			classification: "generic",
			totalCallers:   totalCallers,
			narrowCallers:  0,
		}
	}

	dominantType := narrowSorted[0].typ
	dominantCount := narrowSorted[0].count
	narrowFrac := float64(narrow) / float64(totalCallers)
	domAmongNarrow := float64(dominantCount) / float64(narrow)

	classification := "spread"
	score := float64(narrow) * 0.15

	switch {
	case domAmongNarrow >= 0.95 && narrowFrac >= 0.95:
		classification = "monomorphic"
		score = float64(dominantCount)
	case domAmongNarrow >= 0.95:
		classification = "monomorphic-narrow"
		score = float64(dominantCount)
	case len(narrowSorted) >= 2 &&
		float64(narrowSorted[0].count+narrowSorted[1].count)/float64(narrow) >= 0.85 &&
		narrowSorted[1].count >= int(0.1*float64(narrow)):
		classification = "bimodal"
		score = float64(narrowSorted[0].count+narrowSorted[1].count) * 0.7
	case len(narrowSorted) >= 3 && len(narrowSorted) <= 5 &&
		float64(narrowSorted[0].count+narrowSorted[1].count+narrowSorted[2].count)/float64(narrow) >= 0.7:
		topThree := narrowSorted[0].count + narrowSorted[1].count + narrowSorted[2].count
		classification = "polymorphic"
		score = float64(topThree) * 0.4
	}

	return paramOpportunity{
		classification: classification,
		totalCallers:   totalCallers,
		narrowCallers:  narrow,
		dominantType:   dominantType,
		dominantCount:  dominantCount,
		score:          score,
	}
}

// isWideType returns true for types that carry no specialization signal:
// any, unknown, void, the empty object {}, and never (no-callers buckets).
func isWideType(t string) bool {
	switch t {
	case "any", "unknown", "void", "{}", "never", "object", "Object":
		return true
	}
	return false
}

func truncateType(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func declName(n *ast.Node) string {
	if name := n.Name(); name != nil && name.Kind == ast.KindIdentifier {
		return name.AsIdentifier().Text
	}
	switch n.Kind {
	case ast.KindArrowFunction:
		return "<arrow>"
	case ast.KindFunctionExpression:
		return "<fnexpr>"
	}
	return "<anonymous>"
}

func lineCol(file *ast.SourceFile, pos int) (int, int) {
	line, byteOff := scanner.GetECMALineAndByteOffsetOfPosition(file, pos)
	return line + 1, byteOff + 1
}

func snippet(file *ast.SourceFile, n *ast.Node, maxLen int) string {
	text := file.Text()
	start := scanner.SkipTrivia(text, n.Pos())
	end := n.End()
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	s := text[start:end]
	for i, r := range s {
		if r == '\n' {
			s = s[:i] + "\\n" + s[i+1:]
		}
	}
	if len(s) > maxLen {
		s = s[:maxLen-3] + "..."
	}
	return s
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100 / float64(total)
}
