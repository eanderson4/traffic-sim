// scenario is the ADR-0012 scenario-directory tool: validate (strict-YAML
// fence + semantic checks, fail-loud), fmt (canonical rewrite — diffs stay
// semantic, the content hash stays stable), hash (the content identity half
// of the (content-hash, seed) run key), and migrate (in-place upgrade;
// format_version 1 is the only version so far, so migrate is a canonical
// rewrite — the N/N−1 machinery arrives with v2).
package main

import (
	"flag"
	"fmt"
	"os"

	"traffic-sim/engine/scenario"
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: scenario <command> <dir>

commands:
  validate <dir>  strict-YAML + semantic validation (fail-loud)
  fmt <dir>       canonical rewrite of scenario.yaml and demand parts
  hash <dir>      print the scenario content hash (run key = (hash, seed))
  migrate <dir>   upgrade in place (v1: canonical rewrite only)
`)
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 2 {
		usage()
	}
	cmd, dir := flag.Arg(0), flag.Arg(1)
	switch cmd {
	case "validate":
		if _, err := scenario.Load(dir); err != nil {
			fmt.Fprintln(os.Stderr, "scenario validate:", err)
			os.Exit(1)
		}
		fmt.Printf("%s: valid\n", dir)
	case "fmt", "migrate":
		if err := scenario.Format(dir); err != nil {
			fmt.Fprintf(os.Stderr, "scenario %s: %v\n", cmd, err)
			os.Exit(1)
		}
		fmt.Printf("%s: formatted\n", dir)
	case "hash":
		s, err := scenario.Load(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "scenario hash:", err)
			os.Exit(1)
		}
		fmt.Println(s.Hash())
	default:
		usage()
	}
}
