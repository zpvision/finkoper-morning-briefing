// Counts locally; --verify is an explicit, render-only compatibility check.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"finkoper-morning-briefing/internal/briefing"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	verify := flag.Bool("verify", false, "check Ollama version, digest, template and exact render (no generation fallback)")
	probe := flag.Bool("probe", false, "verify synthetic Unicode/whitespace probe; requires --verify")
	flag.Parse()
	if flag.NArg() == 0 && !*probe {
		return errors.New("usage: budget [--verify] [--probe] examples/01.json ...")
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	if *probe {
		if !*verify {
			return errors.New("--probe requires --verify")
		}
		s, u := briefing.SyntheticProbe()
		if err = counter.Verify(context.Background(), s, u); err != nil {
			return err
		}
		fmt.Println(`{"synthetic_probe_verified":true}`)
	}
	rejected := false
	for _, path := range flag.Args() {
		raw, err := os.ReadFile(path)
		if err != nil {
			return errors.New("cannot_read_input_file")
		}
		var in briefing.Input
		if err = briefing.Strict(raw, &in); err != nil {
			return err
		}
		if err = in.Normalize(); err != nil {
			return err
		}
		s, u := briefing.PromptInput(in)
		if *verify {
			if err = counter.Verify(context.Background(), s, u); err != nil {
				return err
			}
		}
		b, err := briefing.AnalyzeBudget(context.Background(), counter, in)
		if err != nil {
			var exceeded *briefing.BudgetExceeded
			if !errors.As(err, &exceeded) {
				return err
			}
			rejected = true
		}
		out := struct {
			File     string `json:"file"`
			Accepted bool   `json:"accepted"`
			briefing.Budget
		}{path, err == nil, b}
		if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
			return errors.New("output_failed")
		}
	}
	if rejected {
		return errors.New("input_budget_exceeded")
	}
	return nil
}
