package dialer

import (
	"bytes"
	"encoding/binary"
	"strings"
)

func sniffDomain(b []byte) string {
	if host := sniffTLSClientHelloSNI(b); host != "" {
		return host
	}
	return sniffHTTPHost(b)
}

func sniffHTTPHost(b []byte) string {
	if len(b) < 16 || !bytes.Contains(b, []byte("\r\n")) {
		return ""
	}
	lineEnd := bytes.Index(b, []byte("\r\n"))
	if lineEnd <= 0 {
		return ""
	}
	fields := bytes.Fields(b[:lineEnd])
	if len(fields) == 0 {
		return ""
	}
	method := strings.ToUpper(string(fields[0]))
	switch method {
	case "GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT":
	default:
		return ""
	}
	for _, line := range bytes.Split(b[lineEnd+2:], []byte("\r\n")) {
		if len(line) == 0 {
			break
		}
		k, v, ok := bytes.Cut(line, []byte(":"))
		if !ok || !strings.EqualFold(string(k), "host") {
			continue
		}
		host := strings.TrimSpace(string(v))
		host = strings.Trim(host, "[]")
		if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i+1:], ":") {
			host = host[:i]
		}
		return strings.ToLower(host)
	}
	return ""
}

func sniffTLSClientHelloSNI(b []byte) string {
	if len(b) < 5 || b[0] != 0x16 {
		return ""
	}
	recordLen := int(binary.BigEndian.Uint16(b[3:5]))
	if len(b) < 5+recordLen || recordLen < 42 {
		return ""
	}
	p := b[5 : 5+recordLen]
	if len(p) < 4 || p[0] != 0x01 {
		return ""
	}
	hsLen := int(p[1])<<16 | int(p[2])<<8 | int(p[3])
	if len(p) < 4+hsLen {
		return ""
	}
	p = p[4 : 4+hsLen]
	if len(p) < 34 {
		return ""
	}
	p = p[34:] // legacy_version + random
	if len(p) < 1 {
		return ""
	}
	sidLen := int(p[0])
	if len(p) < 1+sidLen+2 {
		return ""
	}
	p = p[1+sidLen:]
	csLen := int(binary.BigEndian.Uint16(p[:2]))
	if len(p) < 2+csLen+1 {
		return ""
	}
	p = p[2+csLen:]
	compLen := int(p[0])
	if len(p) < 1+compLen+2 {
		return ""
	}
	p = p[1+compLen:]
	extLen := int(binary.BigEndian.Uint16(p[:2]))
	if len(p) < 2+extLen {
		return ""
	}
	p = p[2 : 2+extLen]
	for len(p) >= 4 {
		typ := binary.BigEndian.Uint16(p[:2])
		l := int(binary.BigEndian.Uint16(p[2:4]))
		if len(p) < 4+l {
			return ""
		}
		ext := p[4 : 4+l]
		if typ == 0 {
			return parseServerNameExtension(ext)
		}
		p = p[4+l:]
	}
	return ""
}

func parseServerNameExtension(ext []byte) string {
	if len(ext) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(ext[:2]))
	if len(ext) < 2+listLen {
		return ""
	}
	p := ext[2 : 2+listLen]
	for len(p) >= 3 {
		nameType := p[0]
		nameLen := int(binary.BigEndian.Uint16(p[1:3]))
		if len(p) < 3+nameLen {
			return ""
		}
		if nameType == 0 {
			return strings.ToLower(string(p[3 : 3+nameLen]))
		}
		p = p[3+nameLen:]
	}
	return ""
}
