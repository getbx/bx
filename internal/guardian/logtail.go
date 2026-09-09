package guardian

import (
	"bytes"
	"io"
	"os"
)

// tailLines 返回 path 最后 n 行(文件顺序,不含换行符)。
//
// **从文件末尾按块倒读,不整文件读进内存**:/var/log/bx-guard.err.log 真的到过
// 100MB(2026-09-01),而调用方只要最后几百行。文件不存在 / 打不开如实返回 error
// —— 「没读到」与「日志是空的」必须分开(与 Tristate 同一条)。
func tailLines(path string, n int) ([]string, error) {
	if n <= 0 {
		return []string{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const block = 64 << 10
	var buf []byte
	offset := info.Size()
	for offset > 0 && bytes.Count(bytes.TrimRight(buf, "\n"), []byte{'\n'}) < n {
		size := int64(block)
		if offset < size {
			size = offset
		}
		offset -= size
		chunk := make([]byte, size)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(chunk, buf...)
	}
	// 去掉末尾的换行,再按行切;最后一行没有换行也算一行。
	buf = bytes.TrimRight(buf, "\n")
	if len(buf) == 0 {
		return []string{}, nil
	}
	parts := bytes.Split(buf, []byte{'\n'})
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out, nil
}
