package textformat

import (
	"strings"
	"testing"
)

func mustParseSingleSExpr(t *testing.T, input string) *SExpr {
	t.Helper()
	sxs, err := ParseTopLevelSExprs(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(sxs) != 1 {
		t.Fatalf("got %d top-level expressions, want 1", len(sxs))
	}
	return sxs[0]
}

func mustPlainInstr(t *testing.T, instr Instruction) *PlainInstr {
	t.Helper()
	pi, ok := instr.(*PlainInstr)
	if !ok {
		t.Fatalf("expected *PlainInstr, got %T", instr)
	}
	return pi
}

func mustFoldedInstr(t *testing.T, instr Instruction) *FoldedInstr {
	t.Helper()
	fi, ok := instr.(*FoldedInstr)
	if !ok {
		t.Fatalf("expected *FoldedInstr, got %T", instr)
	}
	return fi
}

func mustInstrSeq(t *testing.T, instr Instruction) *InstrSeq {
	t.Helper()
	seq, ok := instr.(*InstrSeq)
	if !ok {
		t.Fatalf("expected *InstrSeq, got %T", instr)
	}
	return seq
}

func showForTest(sx *SExpr) string {
	if sx.IsList() {
		var parts []string
		for _, sub := range sx.list {
			parts = append(parts, showForTest(sub))
		}
		return "(" + strings.Join(parts, " ") + ")"
	}
	return tokenNames[sx.tok.name]
}

func TestSexprSmoke(t *testing.T) {
	s := `(foo bar)`
	sx := mustParseSingleSExpr(t, s)

	if len(sx.list) != 2 {
		t.Errorf("got len %v, want 2", len(sx.list))
	}
	if sx.loc.String() != "1:1" {
		t.Errorf("got loc %s, want 1:1", sx.loc)
	}

	elem0 := sx.list[0]
	if !(elem0.IsToken() && elem0.tok.value == "foo" && elem0.loc.String() == "1:2") {
		t.Errorf("got at 0: %v (loc %s), want token 'foo'", elem0, elem0.loc)
	}
	elem1 := sx.list[1]
	if !(elem1.IsToken() && elem1.tok.value == "bar" && elem1.loc.String() == "1:6") {
		t.Errorf("got at 1: %v (loc %s), want token 'bar'", elem1, elem1.loc)
	}
}

func TestEmptyList(t *testing.T) {
	s := `(foo () bar)`
	sx := mustParseSingleSExpr(t, s)

	elem1 := sx.list[1]
	if !(elem1.IsList() && !elem1.IsToken() && len(elem1.list) == 0 && elem1.loc.String() == "1:6") {
		t.Errorf("got at 1: %v (loc %s), want empty list", elem1, elem1.loc)
	}
}

func TestSexprLists(t *testing.T) {
	var tests = []struct {
		input string
		want  string
	}{
		{`(  foo )`, "(KEYWORD)"},
		{`(  foo ($id "str")  )`, "(KEYWORD (ID STRING))"},
		{`(25 (1.5 "str") foo ($id "str"))`, "(INT (FLOAT STRING) KEYWORD (ID STRING))"},
		{`(((foo)))`, "(((KEYWORD)))"},
		{`(x () (()) y)`, "(KEYWORD () (()) KEYWORD)"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			sx := mustParseSingleSExpr(t, tt.input)

			got := showForTest(sx)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSExprWithoutAnnotations(t *testing.T) {
	sx := mustParseSingleSExpr(t, `((@a) module (@"tag") $m ((@a) func) (@a))`)

	got := sx.WithoutAnnotations()
	if got.HeadKeyword() != "module" {
		t.Fatalf("head=%q, want module", got.HeadKeyword())
	}
	if len(got.Children()) != 3 {
		t.Fatalf("got %d children, want 3", len(got.Children()))
	}
	if !got.Children()[1].IsTokenKind(ID) || got.Children()[1].tok.value != "$m" {
		t.Fatalf("got child[1]=%v, want $m identifier", got.Children()[1])
	}
	if got.Children()[2].HeadKeyword() != "func" {
		t.Fatalf("child[2] head=%q, want func", got.Children()[2].HeadKeyword())
	}
}

func TestParseModuleSExpr_MalformedBareAtStringIsNotStripped(t *testing.T) {
	sx := mustParseSingleSExpr(t, `(module (@ "tag") (func))`)

	_, err := ParseModuleSExpr(sx)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported module field") {
		t.Fatalf("got error %q, want unsupported-module-field error", err.Error())
	}
}

func TestErrorUnterminatedLparen(t *testing.T) {
	var tests = []struct {
		input string
		where string
	}{
		{`(foo`, "1:1"},
		{`     ( (abo) (bobo) (foo ()`, "1:21"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := ParseTopLevelSExprs(tt.input)
			if err == nil {
				t.Fatal("got no error, want error")
			}

			if !strings.Contains(err.Error(), tt.where) {
				t.Errorf("got error %v, want to find %s", err, tt.where)
			}
		})
	}
}

func TestParseSmoke(t *testing.T) {
	// Smoke test for parsing a module, checking the parsed AST without using
	// its textual/debug representation.
	wat := `
	(module $mod
		(func $add (export "add") (param $a i32) (param $b i32) (result i32)
			(local $i i64)
			(local f32)
		)
	)`
	m, err := ParseModule(wat)
	if err != nil {
		t.Fatal(err)
	}

	if m.Id != "$mod" {
		t.Errorf("got mod id %v, want $mod", m.Id)
	}
	if m.loc.String() != "2:2" {
		t.Errorf("got mod loc %s, want 2:2", m.loc)
	}

	func0 := m.Funcs[0]
	if func0.Id != "$add" {
		t.Errorf("got func id %v, want $add", func0.Id)
	}
	if func0.Export == nil || *func0.Export != "add" {
		t.Errorf("got func export %v, want add", func0.Export)
	}

	func0params := func0.TyUse.Params
	if len(func0params) != 2 {
		t.Errorf("got %d params, want 2", len(func0params))
	}
	param0, param1 := func0params[0], func0params[1]
	if param0.Id != "$a" || param0.Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $a i32", param0.Id, param0.Ty)
	}
	if param1.Id != "$b" || param1.Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $b i32", param1.Id, param1.Ty)
	}

	result0 := func0.TyUse.Results[0]
	if result0.Ty.String() != "i32" {
		t.Errorf("got result ty=%s, want i32", result0.Ty)
	}

	if len(func0.Locals) != 2 {
		t.Errorf("got %d locals, want 2", len(func0.Locals))
	}
	local0 := func0.Locals[0]
	if local0.Id != "$i" || local0.Ty.String() != "i64" {
		t.Errorf("got param id=%v ty=%s, want $i i64", local0.Id, local0.Ty)
	}
	local1 := func0.Locals[1]
	if local1.Id != "" || local1.Ty.String() != "f32" {
		t.Errorf("got param id=%v ty=%s, want <empty> f32", local1.Id, local1.Ty)
	}
}

func TestParseModule_StartDeclLoc(t *testing.T) {
	wat := `(module
  (func $main)
  (start $main)
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if m.Start == nil {
		t.Fatal("got nil Start, want start declaration")
	}
	if m.Start.FuncRef != "$main" {
		t.Fatalf("start func ref=%q, want $main", m.Start.FuncRef)
	}
	if got := m.Start.loc.String(); got != "3:3" {
		t.Fatalf("start loc=%q, want 3:3", got)
	}
}

func TestParseModule_LinearAddInstructions(t *testing.T) {
	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}

	f := m.Funcs[0]
	if len(f.Instrs) != 3 {
		t.Fatalf("got %d instructions, want 3", len(f.Instrs))
	}

	instr0 := mustPlainInstr(t, f.Instrs[0])
	if instr0.Name != "local.get" {
		t.Fatalf("instr0 name=%q, want local.get", instr0.Name)
	}
	if got := instr0.Loc(); got != "3:5" {
		t.Fatalf("instr0 loc=%q, want 3:5", got)
	}
	if len(instr0.Operands) != 1 {
		t.Fatalf("instr0 has %d operands, want 1", len(instr0.Operands))
	}
	op0, ok := instr0.Operands[0].(*IdOperand)
	if !ok {
		t.Fatalf("instr0 operand type=%T, want *IdOperand", instr0.Operands[0])
	}
	if op0.Value != "$a" {
		t.Fatalf("instr0 operand value=%q, want $a", op0.Value)
	}
	if got := op0.Loc(); got != "3:15" {
		t.Fatalf("instr0 operand loc=%q, want 3:15", got)
	}

	instr1 := mustPlainInstr(t, f.Instrs[1])
	op1, ok := instr1.Operands[0].(*IdOperand)
	if !ok {
		t.Fatalf("instr1 operand type=%T, want *IdOperand", instr1.Operands[0])
	}
	if instr1.Name != "local.get" || op1.Value != "$b" {
		t.Fatalf("got instr1=(%q, %q), want (local.get, $b)", instr1.Name, op1.Value)
	}

	instr2 := mustPlainInstr(t, f.Instrs[2])
	if instr2.Name != "i32.add" {
		t.Fatalf("instr2 name=%q, want i32.add", instr2.Name)
	}
	if len(instr2.Operands) != 0 {
		t.Fatalf("instr2 operands=%d, want 0", len(instr2.Operands))
	}
}

func TestParseModule_BrTableStopsBeforeNextPlainInstruction(t *testing.T) {
	wat := `
(module
  (func
    block $exit
    br_table $exit 0
    i32.const 7
    drop
    end
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 5; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	brTable := mustPlainInstr(t, f.Instrs[1])
	if brTable.Name != "br_table" {
		t.Fatalf("instr1 name=%q, want br_table", brTable.Name)
	}
	if got, want := len(brTable.Operands), 2; got != want {
		t.Fatalf("br_table operand count=%d, want %d", got, want)
	}
	if op, ok := brTable.Operands[0].(*IdOperand); !ok || op.Value != "$exit" {
		t.Fatalf("br_table operand[0]=%T(%v), want *IdOperand($exit)", brTable.Operands[0], brTable.Operands[0])
	}
	if op, ok := brTable.Operands[1].(*IntOperand); !ok || op.Value != "0" {
		t.Fatalf("br_table operand[1]=%T(%v), want *IntOperand(0)", brTable.Operands[1], brTable.Operands[1])
	}

	next := mustPlainInstr(t, f.Instrs[2])
	if next.Name != "i32.const" {
		t.Fatalf("instr2 name=%q, want i32.const", next.Name)
	}
	if got, want := len(next.Operands), 1; got != want {
		t.Fatalf("i32.const operand count=%d, want %d", got, want)
	}
}

func TestParseModule_BrTableStopsBeforeFollowingFoldedInstruction(t *testing.T) {
	wat := `
(module
  (func
    block $exit
    br_table $exit 0
    (i32.add (i32.const 1) (i32.const 2))
    end
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 4; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	brTable := mustPlainInstr(t, f.Instrs[1])
	if brTable.Name != "br_table" {
		t.Fatalf("instr1 name=%q, want br_table", brTable.Name)
	}
	if got, want := len(brTable.Operands), 2; got != want {
		t.Fatalf("br_table operand count=%d, want %d", got, want)
	}

	add := mustFoldedInstr(t, f.Instrs[2])
	if add.Name != "i32.add" {
		t.Fatalf("instr2 name=%q, want i32.add", add.Name)
	}
	if got, want := len(add.Args), 2; got != want {
		t.Fatalf("i32.add arg count=%d, want %d", got, want)
	}
}

func TestParseModule_PlainRefTestCastConsumesTypeOnly(t *testing.T) {
	// Plain ref.test/ref.cast should consume only the reference-type immediate,
	// leaving stack-producing instructions as separate linear instructions.
	wat := `
(module
  (type $T (struct))
  (func
    ref.test (ref $T)
    local.get 0
    ref.cast anyref
    drop
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 4; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	refTest := mustPlainInstr(t, f.Instrs[0])
	if refTest.Name != "ref.test" {
		t.Fatalf("instr0 name=%q, want ref.test", refTest.Name)
	}
	if got, want := len(refTest.Operands), 1; got != want {
		t.Fatalf("ref.test operand count=%d, want %d", got, want)
	}
	refTestType, ok := refTest.Operands[0].(*TypeOperand)
	if !ok {
		t.Fatalf("ref.test operand type=%T, want *TypeOperand", refTest.Operands[0])
	}
	if got, want := refTestType.Ty.String(), "(ref $T)"; got != want {
		t.Fatalf("ref.test type=%q, want %q", got, want)
	}

	next := mustPlainInstr(t, f.Instrs[1])
	if next.Name != "local.get" {
		t.Fatalf("instr1 name=%q, want local.get", next.Name)
	}

	refCast := mustPlainInstr(t, f.Instrs[2])
	if refCast.Name != "ref.cast" {
		t.Fatalf("instr2 name=%q, want ref.cast", refCast.Name)
	}
	refCastType, ok := refCast.Operands[0].(*TypeOperand)
	if !ok {
		t.Fatalf("ref.cast operand type=%T, want *TypeOperand", refCast.Operands[0])
	}
	if got, want := refCastType.Ty.String(), "anyref"; got != want {
		t.Fatalf("ref.cast type=%q, want %q", got, want)
	}
}

func TestParseModule_PlainCopyInstructionsConsumeIndexPairs(t *testing.T) {
	// Plain memory.copy/table.copy should consume either zero operands or a
	// destination/source pair, without swallowing following stack operands.
	wat := `
(module
  (func
    memory.copy 1 0
    i32.const 7
    table.copy $dst $src
    drop
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 4; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	memCopy := mustPlainInstr(t, f.Instrs[0])
	if memCopy.Name != "memory.copy" {
		t.Fatalf("instr0 name=%q, want memory.copy", memCopy.Name)
	}
	if got, want := len(memCopy.Operands), 2; got != want {
		t.Fatalf("memory.copy operand count=%d, want %d", got, want)
	}
	if op, ok := memCopy.Operands[0].(*IntOperand); !ok || op.Value != "1" {
		t.Fatalf("memory.copy operand[0]=%T(%v), want *IntOperand(1)", memCopy.Operands[0], memCopy.Operands[0])
	}
	if op, ok := memCopy.Operands[1].(*IntOperand); !ok || op.Value != "0" {
		t.Fatalf("memory.copy operand[1]=%T(%v), want *IntOperand(0)", memCopy.Operands[1], memCopy.Operands[1])
	}

	next := mustPlainInstr(t, f.Instrs[1])
	if next.Name != "i32.const" {
		t.Fatalf("instr1 name=%q, want i32.const", next.Name)
	}

	tableCopy := mustPlainInstr(t, f.Instrs[2])
	if tableCopy.Name != "table.copy" {
		t.Fatalf("instr2 name=%q, want table.copy", tableCopy.Name)
	}
	if got, want := len(tableCopy.Operands), 2; got != want {
		t.Fatalf("table.copy operand count=%d, want %d", got, want)
	}
	if op, ok := tableCopy.Operands[0].(*IdOperand); !ok || op.Value != "$dst" {
		t.Fatalf("table.copy operand[0]=%T(%v), want *IdOperand($dst)", tableCopy.Operands[0], tableCopy.Operands[0])
	}
	if op, ok := tableCopy.Operands[1].(*IdOperand); !ok || op.Value != "$src" {
		t.Fatalf("table.copy operand[1]=%T(%v), want *IdOperand($src)", tableCopy.Operands[1], tableCopy.Operands[1])
	}
}

func TestParseModule_PlainOptionalIndexInstructions(t *testing.T) {
	// Plain memory/table ops with optional index immediates should consume one
	// index when present and stop before the next instruction.
	wat := `
(module
  (func
    memory.size 1
    memory.grow $mem
    memory.fill 2
    table.fill $tab
    i32.const 0
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 5; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	tests := []struct {
		index int
		name  string
		want  string
	}{
		{0, "memory.size", "1"},
		{1, "memory.grow", "$mem"},
		{2, "memory.fill", "2"},
		{3, "table.fill", "$tab"},
	}
	for _, tt := range tests {
		instr := mustPlainInstr(t, f.Instrs[tt.index])
		if instr.Name != tt.name {
			t.Fatalf("instr%d name=%q, want %q", tt.index, instr.Name, tt.name)
		}
		if got, want := len(instr.Operands), 1; got != want {
			t.Fatalf("%s operand count=%d, want %d", tt.name, got, want)
		}
		switch op := instr.Operands[0].(type) {
		case *IdOperand:
			if op.Value != tt.want {
				t.Fatalf("%s operand=%q, want %q", tt.name, op.Value, tt.want)
			}
		case *IntOperand:
			if op.Value != tt.want {
				t.Fatalf("%s operand=%q, want %q", tt.name, op.Value, tt.want)
			}
		default:
			t.Fatalf("%s operand type=%T, want ID or INT", tt.name, instr.Operands[0])
		}
	}

	last := mustPlainInstr(t, f.Instrs[4])
	if last.Name != "i32.const" {
		t.Fatalf("instr4 name=%q, want i32.const", last.Name)
	}
}

func TestParseModule_PlainArrayNewFixedTableInitAndElemDrop(t *testing.T) {
	// Plain array.new_fixed, table.init, and elem.drop should consume their
	// declared immediates and leave following stack instructions separate.
	wat := `
(module
  (func
    array.new_fixed $Arr 5
    table.init $t $e
    elem.drop $e
    i32.const 0
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 4; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	arrayNewFixed := mustPlainInstr(t, f.Instrs[0])
	if arrayNewFixed.Name != "array.new_fixed" {
		t.Fatalf("instr0 name=%q, want array.new_fixed", arrayNewFixed.Name)
	}
	if got, want := len(arrayNewFixed.Operands), 2; got != want {
		t.Fatalf("array.new_fixed operand count=%d, want %d", got, want)
	}
	if op, ok := arrayNewFixed.Operands[0].(*IdOperand); !ok || op.Value != "$Arr" {
		t.Fatalf("array.new_fixed operand[0]=%T(%v), want *IdOperand($Arr)", arrayNewFixed.Operands[0], arrayNewFixed.Operands[0])
	}
	if op, ok := arrayNewFixed.Operands[1].(*IntOperand); !ok || op.Value != "5" {
		t.Fatalf("array.new_fixed operand[1]=%T(%v), want *IntOperand(5)", arrayNewFixed.Operands[1], arrayNewFixed.Operands[1])
	}

	tableInit := mustPlainInstr(t, f.Instrs[1])
	if tableInit.Name != "table.init" {
		t.Fatalf("instr1 name=%q, want table.init", tableInit.Name)
	}
	if got, want := len(tableInit.Operands), 2; got != want {
		t.Fatalf("table.init operand count=%d, want %d", got, want)
	}

	elemDrop := mustPlainInstr(t, f.Instrs[2])
	if elemDrop.Name != "elem.drop" {
		t.Fatalf("instr2 name=%q, want elem.drop", elemDrop.Name)
	}
	if got, want := len(elemDrop.Operands), 1; got != want {
		t.Fatalf("elem.drop operand count=%d, want %d", got, want)
	}

	last := mustPlainInstr(t, f.Instrs[3])
	if last.Name != "i32.const" {
		t.Fatalf("instr3 name=%q, want i32.const", last.Name)
	}
}

func TestParseModule_PlainTypedSelectStopsBeforeNextInstruction(t *testing.T) {
	wat := `
(module
  (func
    select (result funcref)
    ref.null func
    drop
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 3; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	// Plain typed select is normalized into the folded parser form so the
	// trailing (result ...) clause can flow through the existing lowering path.
	sel := mustFoldedInstr(t, f.Instrs[0])
	if sel.Name != "select" {
		t.Fatalf("instr0 name=%q, want select", sel.Name)
	}
	if got, want := len(sel.Args), 1; got != want {
		t.Fatalf("select arg count=%d, want %d", got, want)
	}
	clause := mustFoldedInstr(t, sel.Args[0].Instr)
	if clause.Name != "result" {
		t.Fatalf("select clause name=%q, want result", clause.Name)
	}

	next := mustPlainInstr(t, f.Instrs[1])
	if next.Name != "ref.null" {
		t.Fatalf("instr1 name=%q, want ref.null", next.Name)
	}
	last := mustPlainInstr(t, f.Instrs[2])
	if last.Name != "drop" {
		t.Fatalf("instr2 name=%q, want drop", last.Name)
	}
}

func TestParseModule_PlainCallIndirectConsumesTableAndTypeClausesOnly(t *testing.T) {
	wat := `
(module
  (type $sig (func (param i32) (result i64)))
  (table $t 1 funcref)
  (func
    call_indirect $t (type $sig) (param i32) (result i64)
    i32.const 0
    drop
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 3; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	call := mustFoldedInstr(t, f.Instrs[0])
	if call.Name != "call_indirect" {
		t.Fatalf("instr0 name=%q, want call_indirect", call.Name)
	}
	if got, want := len(call.Args), 4; got != want {
		t.Fatalf("call_indirect arg count=%d, want %d", got, want)
	}
	if op, ok := call.Args[0].Operand.(*IdOperand); !ok || op.Value != "$t" {
		t.Fatalf("arg0=%T(%v), want *IdOperand($t)", call.Args[0].Operand, call.Args[0].Operand)
	}
	for i, want := range []string{"type", "param", "result"} {
		clause := mustFoldedInstr(t, call.Args[i+1].Instr)
		if clause.Name != want {
			t.Fatalf("arg%d clause name=%q, want %q", i+1, clause.Name, want)
		}
	}

	next := mustPlainInstr(t, f.Instrs[1])
	if next.Name != "i32.const" {
		t.Fatalf("instr1 name=%q, want i32.const", next.Name)
	}
	last := mustPlainInstr(t, f.Instrs[2])
	if last.Name != "drop" {
		t.Fatalf("instr2 name=%q, want drop", last.Name)
	}
}

func TestParseModule_FoldedInstructions(t *testing.T) {
	wat := `(module
  (func (result i32)
    (i32.add (i32.const 1) (i32.const 2))
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
	f := m.Funcs[0]
	if len(f.Instrs) != 1 {
		t.Fatalf("got %d instructions, want 1 folded instruction", len(f.Instrs))
	}

	root := mustFoldedInstr(t, f.Instrs[0])
	if root.Name != "i32.add" || len(root.Args) != 2 {
		t.Fatalf("root=%#v, want i32.add with 2 nested args", root)
	}

	a0 := mustFoldedInstr(t, root.Args[0].Instr)
	if a0.Name != "i32.const" || len(a0.Args) != 1 {
		t.Fatalf("arg0=%#v, want i32.const with one operand", a0)
	}
	if op, ok := a0.Args[0].Operand.(*IntOperand); !ok || op.Value != "1" {
		t.Fatalf("arg0 operand=%T(%v), want *IntOperand(\"1\")", a0.Args[0].Operand, a0.Args[0].Operand)
	}

	a1 := mustFoldedInstr(t, root.Args[1].Instr)
	if a1.Name != "i32.const" || len(a1.Args) != 1 {
		t.Fatalf("arg1=%#v, want i32.const with one operand", a1)
	}
	if op, ok := a1.Args[0].Operand.(*IntOperand); !ok || op.Value != "2" {
		t.Fatalf("arg1 operand=%T(%v), want *IntOperand(\"2\")", a1.Args[0].Operand, a1.Args[0].Operand)
	}
}

func TestParseModule_Memory64InlineDataShorthand(t *testing.T) {
	wat := `(module
  (memory i64 (data "x"))
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Memories) != 1 {
		t.Fatalf("got %d memories, want 1", len(m.Memories))
	}

	mem := m.Memories[0]
	if mem.AddressType != "i64" {
		t.Fatalf("got address type %q, want i64", mem.AddressType)
	}
	if got := len(mem.InlineData); got != 1 {
		t.Fatalf("got %d inline data strings, want 1", got)
	}
	if mem.InlineData[0] != "x" {
		t.Fatalf("got inline data %q, want x", mem.InlineData[0])
	}
}

func TestParseModule_DataOffsetClause(t *testing.T) {
	wat := `(module
  (data (offset (i32.const 7)) "x")
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Data) != 1 {
		t.Fatalf("got %d data segments, want 1", len(m.Data))
	}
	if m.Data[0].Offset == nil {
		t.Fatal("data offset is nil, want parsed offset expression")
	}
	fi := mustFoldedInstr(t, m.Data[0].Offset)
	if fi.Name != "i32.const" {
		t.Fatalf("offset instruction name=%q, want i32.const", fi.Name)
	}
}

func TestParseModule_FlatConstExprContexts(t *testing.T) {
	wat := `(module
  (global i32 i32.const 1 i32.const 2 i32.add)
  (data (offset i32.const 3 i32.const 4 i32.add) "x")
  (elem (offset i32.const 0 i32.const 0 i32.add) func)
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	globalInit := mustInstrSeq(t, m.Globals[0].Init)
	if len(globalInit.Instrs) != 3 {
		t.Fatalf("global init has %d instructions, want 3", len(globalInit.Instrs))
	}
	dataOffset := mustInstrSeq(t, m.Data[0].Offset)
	if len(dataOffset.Instrs) != 3 {
		t.Fatalf("data offset has %d instructions, want 3", len(dataOffset.Instrs))
	}
	elemOffset := mustInstrSeq(t, m.Elems[0].Offset)
	if len(elemOffset.Instrs) != 3 {
		t.Fatalf("elem offset has %d instructions, want 3", len(elemOffset.Instrs))
	}
}

func TestParseModule_Table64Declaration(t *testing.T) {
	wat := `(module
  (table i64 10 funcref)
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("got %d tables, want 1", len(m.Tables))
	}

	tab := m.Tables[0]
	if tab.AddressType != "i64" {
		t.Fatalf("got address type %q, want i64", tab.AddressType)
	}
	if tab.Min != 10 {
		t.Fatalf("got minimum %d, want 10", tab.Min)
	}
	if tab.RefTy.String() != "funcref" {
		t.Fatalf("got ref type %q, want funcref", tab.RefTy)
	}
}

func TestParseModule_MultiParamAndResultClauses(t *testing.T) {
	wat := `(module
  (func (param i32 i64) (result i32 i64)
    local.get 0
    local.get 1
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}

	f := m.Funcs[0]
	if len(f.TyUse.Params) != 2 {
		t.Fatalf("got %d params, want 2", len(f.TyUse.Params))
	}
	if got := f.TyUse.Params[0].Ty.String(); got != "i32" {
		t.Fatalf("param0 type=%q, want i32", got)
	}
	if got := f.TyUse.Params[1].Ty.String(); got != "i64" {
		t.Fatalf("param1 type=%q, want i64", got)
	}

	if len(f.TyUse.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(f.TyUse.Results))
	}
	if got := f.TyUse.Results[0].Ty.String(); got != "i32" {
		t.Fatalf("result0 type=%q, want i32", got)
	}
	if got := f.TyUse.Results[1].Ty.String(); got != "i64" {
		t.Fatalf("result1 type=%q, want i64", got)
	}
}

func TestParseModule_FoldedCall(t *testing.T) {
	wat := `(module
  (func $callee (result i32)
    (i32.const 42)
  )
  (func (result i32)
    (call $callee)
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Funcs) != 2 {
		t.Fatalf("got %d funcs, want 2", len(m.Funcs))
	}

	f := m.Funcs[1]
	if len(f.Instrs) != 1 {
		t.Fatalf("got %d instructions, want 1", len(f.Instrs))
	}
	call := mustFoldedInstr(t, f.Instrs[0])
	if call.Name != "call" {
		t.Fatalf("instruction name=%q, want call", call.Name)
	}
	if len(call.Args) != 1 {
		t.Fatalf("call has %d args, want 1", len(call.Args))
	}
	op, ok := call.Args[0].Operand.(*IdOperand)
	if !ok || op.Value != "$callee" {
		t.Fatalf("call operand=%T(%v), want *IdOperand($callee)", call.Args[0].Operand, call.Args[0].Operand)
	}
}

func TestParseModule_FoldedIf(t *testing.T) {
	wat := `(module
  (func (param i64) (result i64)
    (if (result i64) (i64.eqz (local.get 0))
      (then (i64.const 1))
      (else (i64.const 2))
    )
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}

	f := m.Funcs[0]
	if len(f.Instrs) != 1 {
		t.Fatalf("got %d instructions, want 1 folded if", len(f.Instrs))
	}
	ifi := mustFoldedInstr(t, f.Instrs[0])
	if ifi.Name != "if" {
		t.Fatalf("root name=%q, want if", ifi.Name)
	}
	if len(ifi.Args) != 4 {
		t.Fatalf("if args=%d, want 4 (result, cond, then, else)", len(ifi.Args))
	}
}

func TestParseModule_PlainIfWithResultClause(t *testing.T) {
	wat := `(module
  (func (param i32 i32) (result i32)
    local.get 0
    local.get 1
    i32.gt_s
    if (result i32)
      i32.const 1
    else
      i32.const 0
    end
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if len(f.Instrs) < 4 {
		t.Fatalf("got %d instructions, want at least 4", len(f.Instrs))
	}
	ifInstr := mustPlainInstr(t, f.Instrs[3])
	if ifInstr.Name != "if" {
		t.Fatalf("instruction name=%q, want if", ifInstr.Name)
	}
	if len(ifInstr.Operands) != 1 {
		t.Fatalf("if operand count=%d, want 1", len(ifInstr.Operands))
	}
	clause, ok := ifInstr.Operands[0].(*StructuredTypeClauseOperand)
	if !ok {
		t.Fatalf("if operand=%T, want *StructuredTypeClauseOperand", ifInstr.Operands[0])
	}
	if clause.Clause != "result" || len(clause.Types) != 1 {
		t.Fatalf("if clause=%#v, want one result type", clause)
	}
}

func TestParseModule_PlainTryTableHeader(t *testing.T) {
	wat := `(module
  (tag $e)
  (func
    block
      try_table (catch $e 0) (catch_all 0)
        nop
      end
    end
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if len(f.Instrs) < 2 {
		t.Fatalf("got %d instructions, want at least 2", len(f.Instrs))
	}
	tryTable := mustPlainInstr(t, f.Instrs[1])
	if tryTable.Name != "try_table" {
		t.Fatalf("instruction name=%q, want try_table", tryTable.Name)
	}
	if len(tryTable.Operands) != 2 {
		t.Fatalf("try_table operands=%d, want 2 catch clauses", len(tryTable.Operands))
	}
	for i, op := range tryTable.Operands {
		if _, ok := op.(*TryTableCatchOperand); !ok {
			t.Fatalf("try_table operand[%d]=%T, want *TryTableCatchOperand", i, op)
		}
	}
}

func TestParseModule_FoldedStructuredBodyAllowsPlainInstructions(t *testing.T) {
	wat := `(module
  (func
    (loop $count (block $done
      (i32.eqz (i32.const 0))
      br_if $done
      br $count))
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	f := m.Funcs[0]
	if len(f.Instrs) != 1 {
		t.Fatalf("got %d instructions, want 1 folded loop", len(f.Instrs))
	}

	loop := mustFoldedInstr(t, f.Instrs[0])
	if loop.Name != "loop" {
		t.Fatalf("root name=%q, want loop", loop.Name)
	}
	if len(loop.Args) != 2 {
		t.Fatalf("loop args=%d, want 2 (label, body block)", len(loop.Args))
	}
	if _, ok := loop.Args[0].Operand.(*IdOperand); !ok {
		t.Fatalf("loop arg[0]=%T, want label operand", loop.Args[0].Operand)
	}

	block := mustFoldedInstr(t, loop.Args[1].Instr)
	if block.Name != "block" {
		t.Fatalf("body name=%q, want block", block.Name)
	}
	if len(block.Args) != 4 {
		t.Fatalf("block args=%d, want 4 (label, folded cond, br_if, br)", len(block.Args))
	}
	if _, ok := block.Args[0].Operand.(*IdOperand); !ok {
		t.Fatalf("block arg[0]=%T, want label operand", block.Args[0].Operand)
	}
	cond := mustFoldedInstr(t, block.Args[1].Instr)
	if cond.Name != "i32.eqz" {
		t.Fatalf("cond name=%q, want i32.eqz", cond.Name)
	}
	brIf := mustPlainInstr(t, block.Args[2].Instr)
	if brIf.Name != "br_if" {
		t.Fatalf("arg[2] name=%q, want br_if", brIf.Name)
	}
	br := mustPlainInstr(t, block.Args[3].Instr)
	if br.Name != "br" {
		t.Fatalf("arg[3] name=%q, want br", br.Name)
	}
}

func TestParseModule_LocalGetWithoutOperandIsRejected(t *testing.T) {
	wat := `(module
  (func
    local.get
  )
)`

	_, err := ParseModule(wat)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "local.get expects one operand") {
		t.Fatalf("got error %q, want local.get missing-operand error", err.Error())
	}
}

func TestParseTopLevelSExprs_Multiple(t *testing.T) {
	src := `(module)
(assert_return (invoke "f"))`

	sxs, err := ParseTopLevelSExprs(src)
	if err != nil {
		t.Fatalf("ParseTopLevelSExprs failed: %v", err)
	}
	if len(sxs) != 2 {
		t.Fatalf("got %d top-level expressions, want 2", len(sxs))
	}
	if got := sxs[0].HeadKeyword(); got != "module" {
		t.Fatalf("first head keyword=%q, want module", got)
	}
	if got := sxs[1].HeadKeyword(); got != "assert_return" {
		t.Fatalf("second head keyword=%q, want assert_return", got)
	}
}

func TestParseModule_LinearFunctionBodyParsesViaTopLevelEntry(t *testing.T) {
	src := `
(module
  (func (param $a i32) (param $b i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	// ParseModule is the normal parser entry point. Internally it still goes
	// through the generic S-expression layer first, even for a simple linear
	// instruction stream like this one.
	m, err := ParseModule(src)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}
	if got, want := len(m.Funcs), 1; got != want {
		t.Fatalf("got %d funcs, want %d", got, want)
	}

	f := m.Funcs[0]
	if got, want := len(f.Instrs), 3; got != want {
		t.Fatalf("got %d instructions, want %d", got, want)
	}

	instr0 := mustPlainInstr(t, f.Instrs[0])
	if instr0.Name != "local.get" {
		t.Fatalf("instr0 name=%q, want local.get", instr0.Name)
	}
	if op, ok := instr0.Operands[0].(*IdOperand); !ok || op.Value != "$a" {
		t.Fatalf("instr0 operand=%T(%v), want *IdOperand($a)", instr0.Operands[0], instr0.Operands[0])
	}

	instr1 := mustPlainInstr(t, f.Instrs[1])
	if instr1.Name != "local.get" {
		t.Fatalf("instr1 name=%q, want local.get", instr1.Name)
	}
	if op, ok := instr1.Operands[0].(*IdOperand); !ok || op.Value != "$b" {
		t.Fatalf("instr1 operand=%T(%v), want *IdOperand($b)", instr1.Operands[0], instr1.Operands[0])
	}

	instr2 := mustPlainInstr(t, f.Instrs[2])
	if instr2.Name != "i32.add" {
		t.Fatalf("instr2 name=%q, want i32.add", instr2.Name)
	}
}

func TestParseModule_MultipleTopLevelExpressionsRejected(t *testing.T) {
	src := `(module) (module)`

	_, err := ParseModule(src)
	if err == nil {
		t.Fatal("expected ParseModule error, got nil")
	}
	if !strings.Contains(err.Error(), "expected exactly one top-level expression") {
		t.Fatalf("got error %q, want top-level expression count error", err.Error())
	}
}

func TestParseModuleSExpr(t *testing.T) {
	src := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	sxs, err := ParseTopLevelSExprs(src)
	if err != nil {
		t.Fatalf("ParseTopLevelSExprs failed: %v", err)
	}
	if len(sxs) != 1 {
		t.Fatalf("got %d top-level expressions, want 1", len(sxs))
	}

	m, err := ParseModuleSExpr(sxs[0])
	if err != nil {
		t.Fatalf("ParseModuleSExpr failed: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
	if got := m.Funcs[0].Export; got == nil || *got != "add" {
		t.Fatalf("func export=%v, want add", got)
	}
}

func TestParseModuleSExpr_AllowsAnnotations(t *testing.T) {
	src := `((@a) module (@"tag") $m
  ((@a) func (@a) (export "add") (@a) (result i32) (@a)
    ((@a) i32.const (@a) 7 (@a))
  )
)`

	sxs, err := ParseTopLevelSExprs(src)
	if err != nil {
		t.Fatalf("ParseTopLevelSExprs failed: %v", err)
	}
	if len(sxs) != 1 {
		t.Fatalf("got %d top-level expressions, want 1", len(sxs))
	}

	m, err := ParseModuleSExpr(sxs[0])
	if err != nil {
		t.Fatalf("ParseModuleSExpr failed: %v", err)
	}
	if got := m.Id; got != "$m" {
		t.Fatalf("module id=%q, want $m", got)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
	if got := m.Funcs[0].Export; got == nil || *got != "add" {
		t.Fatalf("func export=%v, want add", got)
	}
}

func TestParseModule_EmptyListInstructionReportsError(t *testing.T) {
	wat := `(module
  (func
    ()
  )
)`

	_, err := ParseModule(wat)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "expected folded instruction list") {
		t.Fatalf("got error %q, want empty-instruction-list error", err.Error())
	}
}

func TestParseModule_EmptyListTypeReportsError(t *testing.T) {
	wat := `(module
  (func (param ()))
)`

	_, err := ParseModule(wat)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("got error %q, want invalid type error", err.Error())
	}
}

func TestParseModule_EmptyListInstructionDoesNotStopParsing(t *testing.T) {
	wat := `(module
  (func (result i32)
    ()
    (i32.const 1)
  )
)`

	m, err := ParseModule(wat)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "expected folded instruction list") {
		t.Fatalf("got error %q, want empty-instruction-list error", err.Error())
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
	if len(m.Funcs[0].Instrs) != 1 {
		t.Fatalf("got %d instructions, want 1 parsed instruction after empty list", len(m.Funcs[0].Instrs))
	}
	fi := mustFoldedInstr(t, m.Funcs[0].Instrs[0])
	if fi.Name != "i32.const" {
		t.Fatalf("instruction name=%q, want i32.const", fi.Name)
	}
}

func TestParseModule_TypeUseFormsFromSpec(t *testing.T) {
	// Snippet adapted from WebAssembly spec tests (test/core/func.wast).
	wat := `(module
  (type $sig-1 (func))
  (type $sig-2 (func (param i32) (result i32)))

  (func (export "type-use-1") (type $sig-1))
  (func (export "type-use-idx") (type 1))
  (func (export "inline-only") (param i32) (result i32) (local.get 0))
  (func (export "mixed") (type $sig-2) (param i32) (result i32) (local.get 0))
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}

	if len(m.Types) != 2 {
		t.Fatalf("got %d type decls, want 2", len(m.Types))
	}
	if got := m.Types[0].Id; got != "$sig-1" {
		t.Fatalf("type[0] id=%q, want $sig-1", got)
	}
	if len(m.Types[0].TyUse.Params) != 0 || len(m.Types[0].TyUse.Results) != 0 {
		t.Fatalf("type[0] signature got params=%d results=%d, want 0/0", len(m.Types[0].TyUse.Params), len(m.Types[0].TyUse.Results))
	}
	if got := m.Types[1].Id; got != "$sig-2" {
		t.Fatalf("type[1] id=%q, want $sig-2", got)
	}
	if len(m.Types[1].TyUse.Params) != 1 || m.Types[1].TyUse.Params[0].Ty.String() != "i32" {
		t.Fatalf("type[1] params got %#v, want one i32 param", m.Types[1].TyUse.Params)
	}
	if len(m.Types[1].TyUse.Results) != 1 || m.Types[1].TyUse.Results[0].Ty.String() != "i32" {
		t.Fatalf("type[1] results got %#v, want one i32 result", m.Types[1].TyUse.Results)
	}

	if len(m.Funcs) != 4 {
		t.Fatalf("got %d funcs, want 4", len(m.Funcs))
	}

	typeUse1 := m.Funcs[0].TyUse
	if typeUse1.Id != "$sig-1" || len(typeUse1.Params) != 0 || len(typeUse1.Results) != 0 {
		t.Fatalf("func[0] TyUse=%#v, want Id=$sig-1 and empty inline signature", typeUse1)
	}

	typeUseIdx := m.Funcs[1].TyUse
	if typeUseIdx.Id != "1" || len(typeUseIdx.Params) != 0 || len(typeUseIdx.Results) != 0 {
		t.Fatalf("func[1] TyUse=%#v, want Id=1 and empty inline signature", typeUseIdx)
	}

	inlineOnly := m.Funcs[2].TyUse
	if inlineOnly.Id != "" || len(inlineOnly.Params) != 1 || len(inlineOnly.Results) != 1 {
		t.Fatalf("func[2] TyUse=%#v, want inline-only one param/one result", inlineOnly)
	}
	if inlineOnly.Params[0].Ty.String() != "i32" || inlineOnly.Results[0].Ty.String() != "i32" {
		t.Fatalf("func[2] inline signature got param=%s result=%s, want i32/i32", inlineOnly.Params[0].Ty, inlineOnly.Results[0].Ty)
	}

	mixed := m.Funcs[3].TyUse
	if mixed.Id != "$sig-2" || len(mixed.Params) != 1 || len(mixed.Results) != 1 {
		t.Fatalf("func[3] TyUse=%#v, want mixed form with Id=$sig-2 and inline one param/one result", mixed)
	}
	if mixed.Params[0].Ty.String() != "i32" || mixed.Results[0].Ty.String() != "i32" {
		t.Fatalf("func[3] mixed inline signature got param=%s result=%s, want i32/i32", mixed.Params[0].Ty, mixed.Results[0].Ty)
	}
}
