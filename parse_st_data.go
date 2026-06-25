package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Source / Apex SendPropType
var sendPropTypes = map[int]string{
	0:  "DPT_Int",
	1:  "DPT_Float",
	2:  "DPT_Vector",
	3:  "DPT_VectorXY",
	4:  "DPT_String",
	5:  "DPT_Array",
	6:  "DPT_Quaternion",
	7:  "DPT_Int64",
	8:  "DPT_Ticks",
	9:  "DPT_Time",
	10: "DPT_DataTable",
}

var spropFlags = []struct {
	mask int
	name string
}{
	{0x0001, "UNSIGNED"},
	{0x0002, "COORD"},
	{0x0004, "NOSCALE"},
	{0x0008, "ROUNDDOWN"},
	{0x0010, "ROUNDUP"},
	{0x0020, "NORMAL"},
	{0x0040, "EXCLUDE"},
	{0x0080, "XYZE"},
	{0x0100, "INSIDEARRAY"},
	{0x0200, "PROXY_ALWAYS_YES"},
	{0x0400, "IS_A_VECTOR_ELEM"},
	{0x0800, "COLLAPSIBLE"},
	{0x1000, "COORD_MP"},
	{0x2000, "COORD_MP_LOWPRECISION"},
	{0x4000, "COORD_MP_INTEGRAL"},
	{0x8000, "CELL_COORD"},
}

// BitReader implements Source engine bitbuf: little-endian bit packing.
type BitReader struct {
	data       []byte
	bit        int
	totalBits  int
}

func NewBitReader(data []byte, maxBits int) *BitReader {
	if maxBits < 0 || maxBits > len(data)*8 {
		maxBits = len(data) * 8
	}
	return &BitReader{data: data, totalBits: maxBits}
}

func (r *BitReader) ReadBits(n int) (int, error) {
	if n == 0 {
		return 0, nil
	}
	if r.bit+n > r.totalBits {
		return 0, fmt.Errorf("tried to read %d bits at %d, only %d available", n, r.bit, r.totalBits)
	}
	value := 0
	for i := 0; i < n; i++ {
		b := r.data[r.bit>>3]
		bitIdx := r.bit & 7
		value |= ((int(b) >> bitIdx) & 1) << i
		r.bit++
	}
	return value, nil
}

func (r *BitReader) ReadString() (string, error) {
	var sb strings.Builder
	for {
		c, err := r.ReadBits(8)
		if err != nil {
			return "", err
		}
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String(), nil
}

func (r *BitReader) ReadFloat() (float32, error) {
	raw, err := r.ReadBits(32)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(uint32(raw)), nil
}

func (r *BitReader) BitsLeft() int {
	return r.totalBits - r.bit
}

func formatFlags(flags int) string {
	var parts []string
	for _, f := range spropFlags {
		if flags&f.mask != 0 {
			parts = append(parts, f.name)
		}
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, " | ")
}

type Prop struct {
	Type       string  `json:"type"`
	TypeValue  int     `json:"type_value"`
	Name       string  `json:"name"`
	Flags      int     `json:"flags"`
	FlagsStr   string  `json:"flags_str"`
	Priority   int     `json:"priority"`
	DataTable  *string `json:"data_table,omitempty"`
	ExcludeDT  *string `json:"exclude_dt,omitempty"`
	NumElements *int   `json:"num_elements,omitempty"`
	LowValue   *float32 `json:"low_value,omitempty"`
	HighValue  *float32 `json:"high_value,omitempty"`
	NumBits    *int     `json:"num_bits,omitempty"`
}

func parseProp(reader *BitReader) (*Prop, error) {
	propTypeVal, err := reader.ReadBits(5)
	if err != nil {
		return nil, err
	}
	varName, err := reader.ReadString()
	if err != nil {
		return nil, err
	}
	flags, err := reader.ReadBits(16)
	if err != nil {
		return nil, err
	}
	priority, err := reader.ReadBits(8)
	if err != nil {
		return nil, err
	}

	typeName, ok := sendPropTypes[propTypeVal]
	if !ok {
		typeName = fmt.Sprintf("DPT_Unknown(%d)", propTypeVal)
	}

	prop := &Prop{
		Type:      typeName,
		TypeValue: propTypeVal,
		Name:      varName,
		Flags:     flags,
		FlagsStr:  formatFlags(flags),
		Priority:  priority,
	}

	if propTypeVal == 10 {
		dt, err := reader.ReadString()
		if err != nil {
			return nil, err
		}
		prop.DataTable = &dt
	} else if flags&0x40 != 0 {
		ex, err := reader.ReadString()
		if err != nil {
			return nil, err
		}
		prop.ExcludeDT = &ex
	} else if propTypeVal == 5 {
		ne, err := reader.ReadBits(10)
		if err != nil {
			return nil, err
		}
		prop.NumElements = &ne
	} else {
		low, err := reader.ReadFloat()
		if err != nil {
			return nil, err
		}
		high, err := reader.ReadFloat()
		if err != nil {
			return nil, err
		}
		nb, err := reader.ReadBits(7)
		if err != nil {
			return nil, err
		}
		prop.LowValue = &low
		prop.HighValue = &high
		prop.NumBits = &nb
	}

	return prop, nil
}

type SendTable struct {
	Name         string `json:"name"`
	NumProps     int    `json:"num_props"`
	Props        []Prop `json:"props"`
	MsgType      int    `json:"msg_type"`
	NeedsDecoder bool   `json:"needs_decoder"`
	PayloadBits  int    `json:"payload_bits"`
}

type Header struct {
	Magic        string `json:"magic"`
	Version      int    `json:"version"`
	Fingerprint  string `json:"fingerprint"`
	SendtableCRC string `json:"sendtable_crc"`
	BitsWritten  int    `json:"bits_written"`
	FileSize     int    `json:"file_size"`
}

type Result struct {
	Header    Header      `json:"header"`
	NumTables int         `json:"num_tables"`
	Tables    []SendTable `json:"tables"`
}

func parseSendTable(reader *BitReader) (*SendTable, error) {
	tableName, err := reader.ReadString()
	if err != nil {
		return nil, err
	}
	nProps, err := reader.ReadBits(10)
	if err != nil {
		return nil, err
	}
	props := make([]Prop, nProps)
	for i := 0; i < nProps; i++ {
		prop, err := parseProp(reader)
		if err != nil {
			return nil, err
		}
		props[i] = *prop
	}
	return &SendTable{
		Name:     tableName,
		NumProps: nProps,
		Props:    props,
	}, nil
}

func parseStData(data []byte) (*Result, error) {
	if len(data) < 20 {
		return nil, fmt.Errorf("file too small for SSTH header")
	}
	magic := string(data[:4])
	if magic != "SSTH" {
		return nil, fmt.Errorf("bad magic: %q", magic)
	}
	version := int(binary.LittleEndian.Uint32(data[4:8]))
	fingerprint := binary.LittleEndian.Uint32(data[8:12])
	sendtableCRC := binary.LittleEndian.Uint32(data[12:16])
	bitsWritten := int(binary.LittleEndian.Uint32(data[16:20]))

	payload := data[20:]
	reader := NewBitReader(payload, bitsWritten)

	var tables []SendTable
	for reader.BitsLeft() > 7 {
		msgType, err := reader.ReadBits(7)
		if err != nil {
			break
		}
		needsDecoder, err := reader.ReadBits(1)
		if err != nil {
			break
		}
		payloadBits, err := reader.ReadBits(32)
		if err != nil {
			break
		}

		if payloadBits == 0 || payloadBits > reader.BitsLeft() {
			break
		}

		bitOffset := reader.bit & 7
		payloadStartByte := reader.bit / 8
		payloadEndByte := payloadStartByte + (payloadBits+bitOffset+7)/8 + 1
		if payloadEndByte > len(payload) {
			payloadEndByte = len(payload)
		}
		raw := payload[payloadStartByte:payloadEndByte]

		pr := NewBitReader(raw, payloadBits+bitOffset)
		pr.bit = bitOffset

		table, err := parseSendTable(pr)
		if err != nil {
			return nil, fmt.Errorf("parse table %d: %w", len(tables), err)
		}
		table.MsgType = msgType
		table.NeedsDecoder = needsDecoder != 0
		table.PayloadBits = payloadBits
		tables = append(tables, *table)

		reader.bit += payloadBits
	}

	return &Result{
		Header: Header{
			Magic:        magic,
			Version:      version,
			Fingerprint:  fmt.Sprintf("0x%08x", fingerprint),
			SendtableCRC: fmt.Sprintf("0x%08x", sendtableCRC),
			BitsWritten:  bitsWritten,
			FileSize:     len(data),
		},
		NumTables: len(tables),
		Tables:    tables,
	}, nil
}

func dumpText(result *Result, outputPath string) error {
	var lines []string
	h := result.Header
	lines = append(lines, "# SSTH SendTable dump")
	lines = append(lines, fmt.Sprintf("# magic: %s", h.Magic))
	lines = append(lines, fmt.Sprintf("# version: %d", h.Version))
	lines = append(lines, fmt.Sprintf("# fingerprint: %s", h.Fingerprint))
	lines = append(lines, fmt.Sprintf("# sendtable_crc: %s", h.SendtableCRC))
	lines = append(lines, fmt.Sprintf("# bits_written: %d", h.BitsWritten))
	lines = append(lines, fmt.Sprintf("# file_size: %d", h.FileSize))
	lines = append(lines, fmt.Sprintf("# num_tables: %d", result.NumTables))
	lines = append(lines, "")

	for idx, table := range result.Tables {
		lines = append(lines, fmt.Sprintf(
			"SendTable #%d name=%q nProps=%d msg_type=%d needs_decoder=%v payload_bits=%d",
			idx, table.Name, table.NumProps, table.MsgType, table.NeedsDecoder, table.PayloadBits,
		))
		for i, prop := range table.Props {
			var extraParts []string
			if prop.DataTable != nil {
				extraParts = append(extraParts, fmt.Sprintf("dataTable=%q", *prop.DataTable))
			}
			if prop.ExcludeDT != nil {
				extraParts = append(extraParts, fmt.Sprintf("excludeDT=%q", *prop.ExcludeDT))
			}
			if prop.NumElements != nil {
				extraParts = append(extraParts, fmt.Sprintf("nElements=%d", *prop.NumElements))
			}
			if prop.LowValue != nil {
				extraParts = append(extraParts, fmt.Sprintf("low=%g high=%g bits=%d", *prop.LowValue, *prop.HighValue, *prop.NumBits))
			}
			extra := strings.Join(extraParts, " ")
			if extra != "" {
				extra = " " + extra
			}
			lines = append(lines, fmt.Sprintf(
				"  [%d] %s %q flags=0x%04x(%s) priority=%d%s",
				i, prop.Type, prop.Name, prop.Flags, prop.FlagsStr, prop.Priority, extra,
			))
		}
		lines = append(lines, "")
	}

	return os.WriteFile(outputPath, []byte(strings.Join(lines, "\n")), 0644)
}

func main() {
	var inputPath, outputPath string
	if len(os.Args) >= 3 {
		inputPath = os.Args[1]
		outputPath = os.Args[2]
	} else {
		inputPath = `C:\Program Files (x86)\Steam\steamapps\common\Apex Legends\cfg\client\st_data.bin`
		outputPath = "st_data.txt"
	}

	data, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read input: %v\n", err)
		os.Exit(1)
	}

	result, err := parseStData(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	if strings.ToLower(filepath.Ext(outputPath)) == ".json" {
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal json: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(outputPath, out, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write output: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := dumpText(result, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "write output: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Parsed %d send tables from %s -> %s\n", result.NumTables, inputPath, outputPath)
}
