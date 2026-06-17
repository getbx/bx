//go:build linux

package procredact

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
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
	ranges, err := argvRanges(pid)
	if err != nil {
		return err
	}
	if err := attach(pid); err != nil {
		return err
	}
	defer unix.PtraceDetach(pid)
	replacement := bytes.Repeat([]byte{'x'}, len(target))
	found := 0
	for _, r := range ranges {
		n, err := redactRange(pid, r, target, replacement)
		found += n
		if err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("secret not found in process argv")
	}
	return nil
}

func attach(pid int) error {
	if err := unix.PtraceAttach(pid); err != nil {
		return fmt.Errorf("ptrace attach: %w", err)
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(pid, &status, 0, nil); err != nil {
		_ = unix.PtraceDetach(pid)
		return fmt.Errorf("ptrace wait: %w", err)
	}
	return nil
}

func argvRanges(pid int) ([]memRange, error) {
	r, err := argvRangeFromStat(pid)
	if err == nil {
		return []memRange{r}, nil
	}
	ranges, fallbackErr := stackRanges(pid)
	if fallbackErr != nil {
		return nil, err
	}
	return ranges, nil
}

func argvRangeFromStat(pid int) (memRange, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return memRange{}, fmt.Errorf("read process stat: %w", err)
	}
	s := string(b)
	endComm := strings.LastIndex(s, ") ")
	if endComm < 0 {
		return memRange{}, fmt.Errorf("bad process stat")
	}
	fields := strings.Fields(s[endComm+2:])
	const (
		argStartField = 48
		argEndField   = 49
		fieldOffset   = 3
	)
	if len(fields) <= argEndField-fieldOffset {
		return memRange{}, fmt.Errorf("process stat missing argv range")
	}
	start, err := strconv.ParseInt(fields[argStartField-fieldOffset], 10, 64)
	if err != nil {
		return memRange{}, fmt.Errorf("bad argv start: %w", err)
	}
	end, err := strconv.ParseInt(fields[argEndField-fieldOffset], 10, 64)
	if err != nil {
		return memRange{}, fmt.Errorf("bad argv end: %w", err)
	}
	if end <= start {
		return memRange{}, fmt.Errorf("empty argv range")
	}
	return memRange{start: start, end: end}, nil
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

func redactRange(pid int, r memRange, target, replacement []byte) (int, error) {
	const chunkSize = 64 * 1024
	overlap := len(target) - 1
	if overlap < 0 {
		overlap = 0
	}
	buf := make([]byte, chunkSize+overlap)
	var carry []byte
	found := 0
	for off := r.start; off < r.end; off += chunkSize {
		want := chunkSize
		if remaining := int(r.end - off); remaining < want {
			want = remaining
		}
		n, err := readProcess(pid, uintptr(off), buf[len(carry):len(carry)+want])
		if err != nil {
			return found, fmt.Errorf("read process argv: %w", err)
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
			if err := writeProcess(pid, uintptr(abs), replacement); err != nil {
				return found, fmt.Errorf("write process argv: %w", err)
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

func readProcess(pid int, addr uintptr, dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	local := []unix.Iovec{{Base: &dst[0]}}
	local[0].SetLen(len(dst))
	remote := []unix.RemoteIovec{{Base: addr, Len: len(dst)}}
	return unix.ProcessVMReadv(pid, local, remote, 0)
}

func writeProcess(pid int, addr uintptr, src []byte) error {
	if len(src) == 0 {
		return nil
	}
	local := []unix.Iovec{{Base: (*byte)(unsafe.Pointer(&src[0]))}}
	local[0].SetLen(len(src))
	remote := []unix.RemoteIovec{{Base: addr, Len: len(src)}}
	n, err := unix.ProcessVMWritev(pid, local, remote, 0)
	if err != nil {
		return err
	}
	if n != len(src) {
		return fmt.Errorf("short write: %d/%d", n, len(src))
	}
	return nil
}
