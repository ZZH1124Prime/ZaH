// Command generate derives wire Go/TypeScript declarations from schemas/rpc.json.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"sort"
	"strings"
)

type field struct {
	Type   json.RawMessage `json:"type"`
	GoName string          `json:"x-go-name"`
	GoType string          `json:"x-go-type"`
	TSType string          `json:"x-ts-type"`
	Const  json.RawMessage `json:"const"`
}
type definition struct {
	Properties map[string]field `json:"properties"`
	Required   []string         `json:"required"`
}
type schema struct {
	Limits struct {
		Version     int `json:"protocol_version"`
		Frame       int `json:"max_frame_bytes"`
		Connections int `json:"max_connections"`
		Timeout     int `json:"request_timeout_seconds"`
	} `json:"x-zah"`
	Defs map[string]definition `json:"$defs"`
}

func main() {
	raw, err := os.ReadFile("../../schemas/rpc.json")
	must(err)
	var s schema
	must(json.Unmarshal(raw, &s))
	var goOut, ts bytes.Buffer
	goOut.WriteString("// Code generated from schemas/rpc.json; DO NOT EDIT.\npackage transport\nimport (\"encoding/json\";\"time\")\n")
	fmt.Fprintf(&goOut, "const ProtocolVersion = %d\nconst MaxFrameBytes = %d\nconst MaxConnections = %d\nconst DefaultTimeout = %d*time.Second\n", s.Limits.Version, s.Limits.Frame, s.Limits.Connections, s.Limits.Timeout)
	fmt.Fprintf(&ts, "// Code generated from schemas/rpc.json; DO NOT EDIT.\nexport const PROTOCOL_VERSION = %d as const;\nexport const MAX_FRAME_BYTES = %d;\n", s.Limits.Version, s.Limits.Frame)
	names := make([]string, 0, len(s.Defs))
	for name := range s.Defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := s.Defs[name]
		required := map[string]bool{}
		for _, key := range def.Required {
			required[key] = true
		}
		keys := make([]string, 0, len(def.Properties))
		for key := range def.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(&goOut, "// %s is a generated protocol envelope.\ntype %s struct {\n", name, name)
		tsName := name
		if name == "Error" {
			tsName = "RPCError"
		}
		fmt.Fprintf(&ts, "export interface %s {\n", tsName)
		for _, key := range keys {
			p := def.Properties[key]
			goName := p.GoName
			if goName == "" {
				goName = strings.ToUpper(key[:1]) + key[1:]
			}
			gt, tt := p.GoType, p.TSType
			if gt == "" {
				switch string(p.Type) {
				case `"string"`:
					gt = "string"
					tt = "string"
				case `"integer"`:
					gt = "int"
					tt = "number"
				default:
					panic("unsupported field " + key)
				}
			}
			if len(p.Const) > 0 {
				tt = string(p.Const)
			}
			suffix, optional := "", ""
			if !required[key] {
				suffix = ",omitempty"
				optional = "?"
			}
			fmt.Fprintf(&goOut, "%s %s `json:\"%s%s\"`\n", goName, gt, key, suffix)
			fmt.Fprintf(&ts, "  %s%s: %s;\n", key, optional, tt)
		}
		goOut.WriteString("}\n")
		ts.WriteString("}\n")
	}
	formatted, err := format.Source(goOut.Bytes())
	must(err)
	must(os.WriteFile("wire_generated.go", formatted, 0644))
	must(os.WriteFile("../../schemas/rpc.generated.ts", ts.Bytes(), 0644))
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
