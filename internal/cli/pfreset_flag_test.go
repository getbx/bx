package cli

import (
	"flag"
	"testing"

	"github.com/urfave/cli/v2"
)

// `bx run --pf-reset` 三态要原样递进 supervisor.Options.PFReset;默认是 on(与不传一样)。
// 漏了这一跳,dry-run 会变成真装规则 —— 那正是首次真机验证要靠它避开的事。
func TestRunPFResetFlagReachesSupervisorOptions(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "on"},
		{[]string{"--pf-reset", "dry-run"}, "dry-run"},
		{[]string{"--pf-reset", "off"}, "off"},
	} {
		set := flag.NewFlagSet("run", flag.ContinueOnError)
		for _, f := range runFlags() {
			if err := f.Apply(set); err != nil {
				t.Fatal(err)
			}
		}
		if err := set.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		opts := optsFromFlags(cli.NewContext(cli.NewApp(), set, nil))
		if opts.PFReset != tc.want {
			t.Fatalf("args %v → PFReset %q, want %q", tc.args, opts.PFReset, tc.want)
		}
	}
}
