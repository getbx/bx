//go:build linux

package procredact

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type memRange struct {
	start int64
	end   int64
}

// RedactArg overwrites occurrences of secret in a child process stack. It is a
// best-effort guard for tools that must receive secrets through argv.
func RedactArg(pid int, secret string) error {
	target := []byte(secret)
	if len(target) == 0 {
		return nil
	}
	ranges, err := stackRanges(pid)
	if err != nil {
		return err
	}
	mem, err := os.OpenFile(fmt.Sprintf("/proc/%d/mem", pid), os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open process memory: %w", err)
	}
	defer mem.Close()
	replacement := bytes.Repeat([]byte{'x'}, len(target))
	found := 0
	for _, r := range ranges {
		n, err := redactRange(mem, r, target, replacement)
		found += n
		if err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("secret not found in process stack")
	}
	return nil
}

func stackRanges(pid int) ([]memRange, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, fmt.Errorf("read process maps: %w", err)
	}
	var ranges []memRange
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, "[stack]") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		r, err := parseRange(fields[0])
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, r)
	}
	if len(ranges) == 0 {
		return nil, fmt.Errorf("process stack mapping not found")
	}
	return ranges, nil
}

func parseRange(s string) (memRange, error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return memRange{}, fmt.Errorf("bad memory range %q", s)
	}
	start, err := strconv.ParseInt(a, 16, 64)
	if err != nil {
		return memRange{}, fmt.Errorf("bad memory range start %q: %w", a, err)
	}
	end, err := strconv.ParseInt(b, 16, 64)
	if err != nil {
		return memRange{}, fmt.Errorf("bad memory range end %q: %w", b, err)
	}
	if end <= start {
		return memRange{}, fmt.Errorf("empty memory range %q", s)
	}
	return memRange{start: start, end: end}, nil
}

func redactRange(mem *os.File, r memRange, target, replacement []byte) (int, error) {
	const chunkSize = 64 * 1024
	overlap := len(target) - 1
	if overlap < 0 {
		overlap = 0
	}
	buf := make([]byte, chunkSize+overlap)
	var carry []byte
	found := 0
	for off := r.start; off < r.end; off += chunkSize {
		n, err := mem.ReadAt(buf[len(carry):chunkSize+len(carry)], off)
		if err != nil && err != io.EOF {
			return found, fmt.Errorf("read process stack: %w", err)
		}
		if n == 0 {
			continue
		}
		window := make([]byte, 0, len(carry)+n)
		window = append(window, carry...)
		window = append(window, buf[len(carry):len(carry)+n]...)
		for searchStart := 0; searchStart < len(window); {
			idx := bytes.Index(window[searchStart:], target)
			if idx < 0 {
				break
			}
			pos := searchStart + idx
			abs := off - int64(len(carry)) + int64(pos)
			if _, err := mem.WriteAt(replacement, abs); err != nil {
				return found, fmt.Errorf("write process stack: %w", err)
			}
			found++
			searchStart = pos + len(target)
		}
		if overlap > 0 {
			if len(window) > overlap {
				carry = append(carry[:0], window[len(window)-overlap:]...)
			} else {
				carry = append(carry[:0], window...)
			}
		}
	}
	return found, nil
}
