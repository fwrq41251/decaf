package index

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Java .class file constants.
const classMagic = 0xCAFEBABE

// Constant pool tags.
const (
	cpUTF8               = 1
	cpInteger            = 3
	cpFloat              = 4
	cpLong               = 5
	cpDouble             = 6
	cpClass              = 7
	cpString             = 8
	cpFieldref           = 9
	cpMethodref          = 10
	cpInterfaceMethodref = 11
	cpNameAndType        = 12
	cpMethodHandle       = 15
	cpMethodType         = 16
	cpDynamic            = 17
	cpInvokeDynamic      = 18
	cpModule             = 19
	cpPackage            = 20
)

// Access flag masks.
const (
	accPublic    = 0x0001
	accPrivate   = 0x0002
	accProtected = 0x0004
	accStatic    = 0x0008
	accFinal     = 0x0010
	accAbstract  = 0x0400
	accInterface = 0x0200
	accEnum      = 0x4000
)

// classFile holds the parsed data from a .class file that we care about.
type classFile struct {
	AccessFlags uint16
	ThisClass   string   // e.g. "java/util/ArrayList"
	SuperClass  string   // e.g. "java/util/AbstractList"
	Interfaces  []string // e.g. ["java/util/List", "java/io/Serializable"]
	Fields      []classMember
	Methods     []classMember
}

// classMember represents a field or method parsed from a .class file.
type classMember struct {
	AccessFlags uint16
	Name        string // e.g. "size", "add"
	Descriptor  string // e.g. "I", "(Ljava/lang/Object;)Z"
}

// parseClassFile parses a Java .class file from r, extracting the class
// declaration, fields, and methods. It only reads the information needed
// for symbol indexing and skips everything else.
func parseClassFile(r io.Reader) (*classFile, error) {
	// Helper to read big-endian values.
	var u16 uint16
	var u32 uint32

	read16 := func() (uint16, error) {
		if err := binary.Read(r, binary.BigEndian, &u16); err != nil {
			return 0, err
		}
		return u16, nil
	}
	read32 := func() (uint32, error) {
		if err := binary.Read(r, binary.BigEndian, &u32); err != nil {
			return 0, err
		}
		return u32, nil
	}

	// Magic number.
	magic, err := read32()
	if err != nil {
		return nil, fmt.Errorf("reading magic: %w", err)
	}
	if magic != classMagic {
		return nil, fmt.Errorf("invalid class magic: 0x%X", magic)
	}

	// Version (skip).
	if _, err := read16(); err != nil {
		return nil, err
	}
	if _, err := read16(); err != nil {
		return nil, err
	}

	// Constant pool.
	cpCount, err := read16()
	if err != nil {
		return nil, err
	}
	cp, err := readConstantPool(r, int(cpCount))
	if err != nil {
		return nil, fmt.Errorf("reading constant pool: %w", err)
	}

	// Access flags.
	accessFlags, err := read16()
	if err != nil {
		return nil, err
	}

	// This class.
	thisIdx, err := read16()
	if err != nil {
		return nil, err
	}
	thisClass := cp.className(int(thisIdx))

	// Super class.
	superIdx, err := read16()
	if err != nil {
		return nil, err
	}
	superClass := cp.className(int(superIdx))

	// Interfaces.
	ifaceCount, err := read16()
	if err != nil {
		return nil, err
	}
	interfaces := make([]string, ifaceCount)
	for i := range interfaces {
		idx, err := read16()
		if err != nil {
			return nil, err
		}
		interfaces[i] = cp.className(int(idx))
	}

	// Fields.
	fields, err := readMembers(r, cp)
	if err != nil {
		return nil, fmt.Errorf("reading fields: %w", err)
	}

	// Methods.
	methods, err := readMembers(r, cp)
	if err != nil {
		return nil, fmt.Errorf("reading methods: %w", err)
	}

	return &classFile{
		AccessFlags: accessFlags,
		ThisClass:   thisClass,
		SuperClass:  superClass,
		Interfaces:  interfaces,
		Fields:      fields,
		Methods:     methods,
	}, nil
}

// constantPool holds the parsed constant pool entries.
type constantPool struct {
	utf8s   map[int]string // index -> UTF-8 string
	classes map[int]int    // index -> name_index (pointing to utf8)
}

func readConstantPool(r io.Reader, count int) (*constantPool, error) {
	cp := &constantPool{
		utf8s:   make(map[int]string),
		classes: make(map[int]int),
	}

	buf := make([]byte, 8) // reusable buffer for skipping fixed-size entries

	// Constant pool indices are 1-based, and Long/Double take two slots.
	for i := 1; i < count; i++ {
		if _, err := io.ReadFull(r, buf[:1]); err != nil {
			return nil, fmt.Errorf("tag at index %d: %w", i, err)
		}
		tag := buf[0]

		switch tag {
		case cpUTF8:
			if _, err := io.ReadFull(r, buf[:2]); err != nil {
				return nil, err
			}
			length := binary.BigEndian.Uint16(buf[:2])
			data := make([]byte, length)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, err
			}
			cp.utf8s[i] = string(data)

		case cpClass:
			if _, err := io.ReadFull(r, buf[:2]); err != nil {
				return nil, err
			}
			nameIdx := int(binary.BigEndian.Uint16(buf[:2]))
			cp.classes[i] = nameIdx

		case cpInteger, cpFloat:
			if _, err := io.ReadFull(r, buf[:4]); err != nil {
				return nil, err
			}

		case cpLong, cpDouble:
			if _, err := io.ReadFull(r, buf[:8]); err != nil {
				return nil, err
			}
			i++ // takes two slots

		case cpString, cpMethodType, cpModule, cpPackage:
			if _, err := io.ReadFull(r, buf[:2]); err != nil {
				return nil, err
			}

		case cpFieldref, cpMethodref, cpInterfaceMethodref, cpNameAndType, cpDynamic, cpInvokeDynamic:
			if _, err := io.ReadFull(r, buf[:4]); err != nil {
				return nil, err
			}

		case cpMethodHandle:
			if _, err := io.ReadFull(r, buf[:3]); err != nil {
				return nil, err
			}

		default:
			return nil, fmt.Errorf("unknown constant pool tag %d at index %d", tag, i)
		}
	}

	return cp, nil
}

// className returns the class name for a CONSTANT_Class_info index.
func (cp *constantPool) className(idx int) string {
	if idx == 0 {
		return ""
	}
	nameIdx, ok := cp.classes[idx]
	if !ok {
		return ""
	}
	return cp.utf8s[nameIdx]
}

// utf8 returns the UTF-8 string at the given constant pool index.
func (cp *constantPool) utf8(idx int) string {
	return cp.utf8s[idx]
}

// readMembers reads a field_info[] or method_info[] section.
func readMembers(r io.Reader, cp *constantPool) ([]classMember, error) {
	var count uint16
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, err
	}

	members := make([]classMember, 0, count)
	for i := 0; i < int(count); i++ {
		var flags, nameIdx, descIdx, attrCount uint16
		if err := binary.Read(r, binary.BigEndian, &flags); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.BigEndian, &nameIdx); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.BigEndian, &descIdx); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.BigEndian, &attrCount); err != nil {
			return nil, err
		}

		// Skip attributes.
		for j := 0; j < int(attrCount); j++ {
			if err := skipAttribute(r); err != nil {
				return nil, err
			}
		}

		members = append(members, classMember{
			AccessFlags: flags,
			Name:        cp.utf8(int(nameIdx)),
			Descriptor:  cp.utf8(int(descIdx)),
		})
	}
	return members, nil
}

// skipAttribute skips a single attribute_info structure.
func skipAttribute(r io.Reader) error {
	var nameIdx uint16
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &nameIdx); err != nil {
		return err
	}
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return err
	}
	_, err := io.CopyN(io.Discard, r, int64(length))
	return err
}

// descriptorToSymbol converts a single Java field type descriptor to a
// SemanticDB symbol string. Examples:
//
//	"I"                     → "" (primitive)
//	"Ljava/util/List;"      → "java/util/List#"
//	"[Ljava/lang/String;"   → "java/lang/String#" (array element type)
func descriptorToSymbol(desc string) string {
	desc = strings.TrimLeft(desc, "[") // strip array dimensions
	if len(desc) == 0 {
		return ""
	}
	if desc[0] == 'L' && desc[len(desc)-1] == ';' {
		return desc[1:len(desc)-1] + "#"
	}
	return "" // primitive
}

// descriptorToSimpleName converts a type descriptor to a human-readable name.
func descriptorToSimpleName(desc string) string {
	arrays := 0
	for strings.HasPrefix(desc, "[") {
		arrays++
		desc = desc[1:]
	}
	base := descriptorBaseToName(desc)
	return base + strings.Repeat("[]", arrays)
}

func descriptorBaseToName(desc string) string {
	switch {
	case desc == "B":
		return "byte"
	case desc == "C":
		return "char"
	case desc == "D":
		return "double"
	case desc == "F":
		return "float"
	case desc == "I":
		return "int"
	case desc == "J":
		return "long"
	case desc == "S":
		return "short"
	case desc == "Z":
		return "boolean"
	case desc == "V":
		return "void"
	case len(desc) > 1 && desc[0] == 'L' && desc[len(desc)-1] == ';':
		// "Ljava/util/List;" → "List"
		inner := desc[1 : len(desc)-1]
		if idx := strings.LastIndex(inner, "/"); idx >= 0 {
			return inner[idx+1:]
		}
		return inner
	default:
		return desc
	}
}

// parseMethodDescriptor splits a method descriptor like "(Ljava/lang/String;I)V"
// into parameter descriptors and a return descriptor.
func parseMethodDescriptor(desc string) (params []string, ret string) {
	if len(desc) == 0 || desc[0] != '(' {
		return nil, ""
	}
	i := 1
	for i < len(desc) && desc[i] != ')' {
		end := scanDescriptor(desc, i)
		if end <= i {
			break
		}
		params = append(params, desc[i:end])
		i = end
	}
	if i < len(desc) && desc[i] == ')' {
		ret = desc[i+1:]
	}
	return
}

// scanDescriptor returns the end index of a single type descriptor starting at pos.
func scanDescriptor(desc string, pos int) int {
	if pos >= len(desc) {
		return pos
	}
	switch desc[pos] {
	case 'B', 'C', 'D', 'F', 'I', 'J', 'S', 'Z', 'V':
		return pos + 1
	case '[':
		return scanDescriptor(desc, pos+1)
	case 'L':
		semi := strings.IndexByte(desc[pos:], ';')
		if semi < 0 {
			return len(desc)
		}
		return pos + semi + 1
	default:
		return pos + 1
	}
}

// formatMethodSignature builds a human-readable signature label from a method descriptor.
// e.g. "(Ljava/lang/String;I)V" + name "foo" → "void foo(String, int)"
func formatMethodSignature(name, desc string) *SignatureInfo {
	params, ret := parseMethodDescriptor(desc)

	retName := descriptorToSimpleName(ret)
	var paramLabels []string
	for _, p := range params {
		paramLabels = append(paramLabels, descriptorToSimpleName(p))
	}

	var b strings.Builder
	b.WriteString(retName)
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString("(")
	b.WriteString(strings.Join(paramLabels, ", "))
	b.WriteString(")")

	return &SignatureInfo{
		Label:     b.String(),
		HasParams: len(paramLabels) > 0,
	}
}
