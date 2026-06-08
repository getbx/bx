package embedded

import "testing"

func TestAssetsPresent(t *testing.T) {
	if len(Brook()) == 0 {
		t.Error("brook 资产为空")
	}
	if len(ChinaDomain()) == 0 {
		t.Error("china_domain 资产为空")
	}
	if len(ChinaCIDR()) == 0 {
		t.Error("china_cidr 资产为空")
	}
	if BrookVersion() == "" {
		t.Error("BROOK_VERSION 为空")
	}
}
