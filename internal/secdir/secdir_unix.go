//go:build unix

package secdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// Ensure 确保 path 存在、是真目录、属主为 ownerUID、且 group/other 不可写。
// 已存在则校验并按 mode 归一化权限(不受 umask 影响);不存在则创建。
// mode 必须不含 group/other 写位,否则返回错误。
//
// 实现全程 fd 锚定:从 "/" 起逐段 openat(O_RDONLY|O_DIRECTORY|O_NOFOLLOW|
// O_CLOEXEC) 打开父链,任何一段是符号链接都会让 open 失败——即便父目录
// (如 macOS 的 /var/run,出厂 0775 root:daemon)可被组写,也无法通过预置
// 符号链接把我们的目录劫持到别处。末段占位若不是「属主目录」(而是符号
// 链接或普通文件),一律返回错误,绝不自动删除,留给人工判断。
func Ensure(path string, ownerUID int, mode os.FileMode) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if err := ValidateMode(mode); err != nil {
		return err
	}

	clean := normalizeDarwinPrefix(path)
	parentPath, name := filepath.Split(clean)
	if err := validateComponentName(name); err != nil {
		return fmt.Errorf("secdir: %q: %w", path, err)
	}

	parentFD, err := openAbsoluteDir(parentPath)
	if err != nil {
		return fmt.Errorf("secdir: open parent of %q: %w", path, err)
	}
	defer func() { _ = unix.Close(parentFD) }()

	fd, err := ensureChildDir(parentFD, name, ownerUID, mode)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return nil
}

// ensureChildDir 在 parentFD 下确保子目录 name 存在、归属 ownerUID、权限归一化
// 为 mode。返回打开的目录 fd(调用方负责 Close)。
func ensureChildDir(parentFD int, name string, ownerUID int, mode os.FileMode) (int, error) {
	if err := validateComponentName(name); err != nil {
		return -1, err
	}

	if err := unix.Mkdirat(parentFD, name, uint32(mode.Perm())); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("secdir: mkdir %q: %w", name, err)
		}
	}

	// O_NOFOLLOW 使符号链接占位在这里直接失败(ELOOP);O_DIRECTORY 使非目录
	// 占位(普通文件)失败(ENOTDIR)。两者都不删除占位,原样返回错误。
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("secdir: open %q: %w", name, err)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("secdir: fstat %q: %w", name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("secdir: %q exists and is not a directory", name)
	}
	if int(stat.Uid) != ownerUID {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("secdir: %q owned by uid %d, want %d", name, stat.Uid, ownerUID)
	}

	// Fchmod 压掉 umask 与历史遗留的组/其他可写位,使权限精确等于 mode。
	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("secdir: chmod %q: %w", name, err)
	}

	var verify unix.Stat_t
	if err := unix.Fstat(fd, &verify); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("secdir: re-fstat %q: %w", name, err)
	}
	if os.FileMode(verify.Mode).Perm()&disallowedModeBits != 0 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("secdir: %q still group/other writable after chmod", name)
	}

	return fd, nil
}

// openAbsoluteDir 从 "/" 逐段 openat(O_NOFOLLOW) 打开 dir(clean、绝对路径的目
// 录),父链上任何一段是符号链接都会导致失败,不会被解引用穿越。
func openAbsoluteDir(dir string) (int, error) {
	clean := filepath.Clean(dir)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if clean == "/" {
		return fd, nil
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if component == "" {
			continue
		}
		if err := validateComponentName(component); err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		childFD, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, fmt.Errorf("open %q: %w", component, err)
		}
		fd = childFD
	}
	return fd, nil
}

func validateComponentName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return fmt.Errorf("invalid path component %q", name)
	}
	return nil
}

// normalizeDarwinPrefix 把 macOS 上 "/var"、"/tmp" 这两个符号链接前缀规范成其
// 真实路径 "/private/var"、"/private/tmp"——否则 O_NOFOLLOW 会在这一步就拒绝
// 打开(/var、/tmp 本身是指向 /private/var、/private/tmp 的符号链接;t.TempDir()
// 在 macOS 上落在 /var/folders/... 下,若不归一化会被自己的防御机制误伤)。
// 仅在 darwin 生效;Linux 上 /var、/tmp 是真实目录,不做改写。
func normalizeDarwinPrefix(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/var", "/tmp"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return "/private" + clean
		}
	}
	return clean
}
