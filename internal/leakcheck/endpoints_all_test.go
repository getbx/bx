package leakcheck

import (
	"reflect"
	"strings"
	"testing"
)

// **披露清单里的每一个端点都必须出现在渲染出来的那一行里。**
//
// 这条守卫是真机跑出来的:trace 端点加进 EndpointDisclosure 之后,页面老老实实列了
// 四个,而 CLI 的 `Contacted:` 那行只列三个 —— 它是手写拼接的,加字段时没人跟着改。
// 少报一个第三方不是排版问题,**是把一个用户可见契约打了折**:那一行的全部意义
// 就是「联网之前把要联系的人说全」。
//
// 判据用反射遍历结构体,而不是手抄一份字段名:手抄的那份下次加字段照样会漏,
// 而漏掉的表现与今天一模一样 —— 安静。
func TestEveryDisclosedEndpointIsRendered(t *testing.T) {
	d := Endpoints()
	line := strings.Join(d.All(), " ")

	v := reflect.ValueOf(d)
	if v.NumField() == 0 {
		t.Fatal("EndpointDisclosure 没有字段 —— 这条守卫已经读不懂它要守的东西")
	}
	for i := 0; i < v.NumField(); i++ {
		value := v.Field(i).String()
		if strings.TrimSpace(value) == "" {
			t.Errorf("%s 是空的 —— 披露清单里不该有空项", v.Type().Field(i).Name)
			continue
		}
		if !strings.Contains(line, value) {
			t.Errorf("端点 %s = %q 没有出现在披露行里:%q —— 少报一个第三方就是把"+
				"「联网之前说全」这个承诺打了折", v.Type().Field(i).Name, value, line)
		}
	}
}
