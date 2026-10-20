package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

func main() {
	for _, path := range os.Args[1:] {
		content, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.UseNumber()
		var value any
		err = decoder.Decode(&value)
		if err != nil {
			log.Fatal(E.Cause(err, "decode ", path))
		}
		var buffer bytes.Buffer
		writeValue(&buffer, value, 0)
		if bytes.Equal(buffer.Bytes(), content) {
			continue
		}
		err = os.WriteFile(path, buffer.Bytes(), 0o644)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func writeValue(buffer *bytes.Buffer, value any, depth int) {
	switch value := value.(type) {
	case map[string]any:
		buffer.WriteByte('{')
		for index, key := range slices.Sorted(maps.Keys(value)) {
			if index > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteString("\n" + strings.Repeat("  ", depth+1))
			writeString(buffer, key)
			buffer.WriteString(" : ")
			writeValue(buffer, value[key], depth+1)
		}
		if len(value) == 0 {
			buffer.WriteByte('\n')
		}
		buffer.WriteString("\n" + strings.Repeat("  ", depth) + "}")
	case []any:
		buffer.WriteByte('[')
		for index, element := range value {
			if index > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteString("\n" + strings.Repeat("  ", depth+1))
			writeValue(buffer, element, depth+1)
		}
		if len(value) == 0 {
			buffer.WriteByte('\n')
		}
		buffer.WriteString("\n" + strings.Repeat("  ", depth) + "]")
	case string:
		writeString(buffer, value)
	case json.Number:
		buffer.WriteString(value.String())
	case bool:
		buffer.WriteString(strconv.FormatBool(value))
	case nil:
		buffer.WriteString("null")
	}
}

func writeString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			buffer.WriteString(`\"`)
		case '\\':
			buffer.WriteString(`\\`)
		case '\b':
			buffer.WriteString(`\b`)
		case '\f':
			buffer.WriteString(`\f`)
		case '\n':
			buffer.WriteString(`\n`)
		case '\r':
			buffer.WriteString(`\r`)
		case '\t':
			buffer.WriteString(`\t`)
		default:
			if char < 0x20 {
				fmt.Fprintf(buffer, `\u%04x`, char)
			} else {
				buffer.WriteRune(char)
			}
		}
	}
	buffer.WriteByte('"')
}
