package rulereview

import (
	"encoding/json"
	"testing"
)

// **往返**:Guardian 算好、CLI 读回来渲染,是 2026-08-31 起真实存在的一条路
// (此前 MarshalJSON 的注释写着「今天不可达」)。四类都必须能原样回来 ——
// 一类在往返中变成另一类,就是把一条安全告警渲染成一条可以删的冗余。
func TestClassSurvivesAJSONRoundTrip(t *testing.T) {
	for _, class := range []Class{ClassRisky, ClassShadowedByUserRule, ClassOverriddenByOppositeKind, ClassShadowedByBuiltinList, ClassDead} {
		raw, err := json.Marshal(class)
		if err != nil {
			t.Fatal(err)
		}
		var back Class
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s 读不回来: %v", class, err)
		}
		if back != class {
			t.Fatalf("往返之后 %s 变成了 %s", class, back)
		}
	}
}

// **认不出的词落到 ClassRisky**,方向与零值那条刻意的不对称一致:
// 新版 Guardian 发来一类旧版 CLI 不认识的结论时,多报一条安全告警是可接受的,
// 反过来(降级成「可以删的冗余」)会让人删掉一条真正危险的规则。
func TestUnknownClassWordFailsTowardsRisky(t *testing.T) {
	var got Class
	if err := json.Unmarshal([]byte(`"something_new"`), &got); err != nil {
		t.Fatalf("认不出的词不该报错(那会让整份报告读不出来): %v", err)
	}
	if got != ClassRisky {
		t.Fatalf("认不出的词落到了 %s,而不是偏安全的 risky", got)
	}
}
