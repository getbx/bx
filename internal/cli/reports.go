package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/getbx/bx/internal/elevate"
	"github.com/getbx/bx/internal/report"
)

// reportsDir 是 Guardian 落问题报告的目录(与 internal/guardian/reporter.go 的 reportsDir
// 同一个路径;那边是常量,这边给 CLI 读)。
const reportsDir = "/var/lib/bx/reports"

// reportsNotice 是 `bx setup` 结束时那一句:上报默认开,发去哪、留在哪、怎么关。
// 所有者定的「无感」默认 —— 无感不等于不告知,这一句就是告知。
func reportsNotice() string {
	return "Problem reports: when bx fails, it sends a redacted report to the bx maintainer (through the tunnel only). " +
		"Copies stay in " + reportsDir + " (bx reports); turn it off with reports: off in the config."
}

func reportsCommand() *cli.Command {
	return &cli.Command{
		Name:     "reports",
		Category: "Diagnose",
		Usage:    "list the problem reports bx kept locally (what was sent to the maintainer, and what is still queued)",
		Description: "When bx fails (protection needs attention, the core cannot start, a recovery gives up, an update rolls back),\n" +
			"Guardian writes a redacted report to " + reportsDir + " and sends it to the bx maintainer through the tunnel.\n" +
			"Server addresses, links and bypass ranges never enter a report. This lists those files; `bx reports show <name>` prints one.\n" +
			"Turn reporting off with `reports: off` in the config.",
		Action: reportsListAction,
		Subcommands: []*cli.Command{
			{Name: "show", Usage: "print one report", ArgsUsage: "<name>", Action: reportsShowAction},
		},
	}
}

func reportsListAction(c *cli.Context) error {
	st := report.NewStore(reportsDir, 50)
	all, err := st.List()
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("reading %s needs administrator rights: "+elevate.Prefix+"bx reports", reportsDir)
		}
		return err
	}
	if len(all) == 0 {
		fmt.Println("No problem reports. bx writes one only when something fails; the folder is " + reportsDir + ".")
		return nil
	}
	fmt.Printf("%-10s %-22s %s\n", "STATE", "WHEN (UTC)", "NAME")
	for _, e := range all {
		when := ""
		if !e.At.IsZero() {
			when = e.At.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-10s %-22s %s\n", e.State, when, e.Name)
	}
	fmt.Println("\nbx reports show <name> prints one. sent = the maintainer has it; pending = waiting for the tunnel; rejected = the collector refused it.")
	return nil
}

func reportsShowAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		return fmt.Errorf("usage: bx reports show <name> (see bx reports)")
	}
	st := report.NewStore(reportsDir, 50)
	raw, err := st.Read(name)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("reading %s needs administrator rights: "+elevate.Prefix+"bx reports show %s", reportsDir, name)
		}
		return err
	}
	os.Stdout.Write(raw)
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		fmt.Println()
	}
	return nil
}
