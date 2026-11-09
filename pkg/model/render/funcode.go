package render

import (
	"strconv"

	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
)

// renderFunctionEntryLeaf renders the code of a Starlark function as a
// table containing its disassembled bytecode, followed by that of the
// functions nested inside it. It returns nil if the code is malformed,
// so that its raw contents are displayed instead.
func renderFunctionEntryLeaf(leaf *model_starlark_pb.CompiledProgram_FunctionEntry_Leaf) Doc {
	if leaf.Code == nil {
		return nil
	}
	// The line at which the function is declared is not stored in
	// this message. Display lines relative to it, with the
	// declaration being on line 1.
	funcode, err := model_starlark.DecodeFuncode(leaf.Code, leaf.Name.GetName(), 1)
	if err != nil {
		return nil
	}
	disassembly, err := funcode.Disassemble()
	if err != nil {
		return nil
	}
	return CompactTable(
		[]TableColumn{
			{Name: "Offset", AlignRight: true},
			{Name: "Position"},
			{Name: "Instruction"},
			{Name: "Operand", AlignRight: true},
			{Name: "Description"},
		},
		appendDisassemblyRows(nil, disassembly),
	)
}

func appendDisassemblyRows(rows [][]Doc, disassembly *compile.Disassembly) [][]Doc {
	rows = append(rows, []Doc{Styled(RoleCallee, disassembly.Signature)})
	for _, detail := range disassembly.Details {
		rows = append(rows, []Doc{Styled(RoleDim, detail)})
	}
	for _, instruction := range disassembly.Instructions {
		operand, comment, position := Text(""), Text(""), Text("")
		if instruction.Opcode >= compile.OpcodeArgMin {
			operand = Styled(RoleLiteral, strconv.FormatUint(uint64(instruction.Operand), 10))
			comment = Styled(RoleDim, instruction.OperandComment)
		}
		if instruction.Position.IsValid() {
			position = Styled(RoleDim, instruction.Position.String())
		}
		rows = append(rows, []Doc{
			Styled(RoleDim, strconv.FormatUint(uint64(instruction.PC), 10)),
			position,
			Text(instruction.Opcode.String()),
			operand,
			comment,
		})
	}
	for _, nested := range disassembly.Functions {
		rows = append(rows, []Doc{Text("")})
		rows = appendDisassemblyRows(rows, nested)
	}
	return rows
}
