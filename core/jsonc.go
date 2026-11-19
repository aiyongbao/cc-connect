package core

import "bytes"

// StripJSONC removes // and /* */ comments from JSONC, preserving strings.
func StripJSONC(data []byte) []byte {
	var out bytes.Buffer
	inString := false
	escaped := false
	i := 0
	for i < len(data) {
		ch := data[i]

		if escaped {
			out.WriteByte(ch)
			escaped = false
			i++
			continue
		}

		if inString {
			out.WriteByte(ch)
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			i++
			continue
		}

		// Not in string.
		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			i++
			continue
		}

		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				// Line comment — skip to end of line.
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
				continue
			}
			if next == '*' {
				// Block comment — skip to closing */.
				i += 2
				closed := false
				for i+1 < len(data) {
					if data[i] == '*' && data[i+1] == '/' {
						i += 2
						closed = true
						break
					}
					i++
				}
				if !closed {
					return data // return original; json.Unmarshal will give a clear error
				}
				continue
			}
		}

		out.WriteByte(ch)
		i++
	}
	return out.Bytes()
}
