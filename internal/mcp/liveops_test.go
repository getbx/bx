package mcp

import "testing"

func TestMutatingRequiresRoot(t *testing.T) {
	// requireRoot 为纯函数:isRoot=false 且 mutating 时返回 PRIVILEGE_REQUIRED。
	if err := requireRoot(false); err == nil {
		t.Fatal("非 root 调改动类应报 PRIVILEGE_REQUIRED")
	} else {
		if te, ok := err.(ToolError); !ok || te.Code != CodePrivilegeRequired {
			t.Fatalf("应为 PRIVILEGE_REQUIRED,得到 %v", err)
		}
	}
	if err := requireRoot(true); err != nil {
		t.Fatalf("root 时不应报错,得到 %v", err)
	}
}
