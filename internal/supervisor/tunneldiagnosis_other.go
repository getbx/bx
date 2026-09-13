//go:build !windows

package supervisor

// platformDialFailuresBeforeTheSYNLeaves:非 Windows 平台没有孪生值 ——
// posix 那四个 errno 在这里就是内核真正返回的东西。
//
// 显式 nil 而不是「让 Windows 那份直接替换整张表」:darwin/linux 的行为必须与
// 加这张表之前**逐字节相同**,append(base, nil...) 保证了这一点。
var platformDialFailuresBeforeTheSYNLeaves []error
