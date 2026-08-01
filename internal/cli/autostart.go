package cli

import "fmt"

// parseAutostartArg 把 `bx autostart <arg>` 的参数解析成意图。
// "on"→want=true;"off"→want=false;"status"/空→status=true;其它→err。
func parseAutostartArg(arg string) (want *bool, status bool, err error) {
	switch arg {
	case "on":
		v := true
		return &v, false, nil
	case "off":
		v := false
		return &v, false, nil
	case "", "status":
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("未知参数 %q(用 on|off|status)", arg)
	}
}
