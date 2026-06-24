// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// runOpenEnvRepl starts an interactive read-eval-print loop that drives the
// OpenEnv contract (reset/step/state) against the supplied client. It reads
// commands from in and writes results to out. The loop exits when the user
// types "quit"/"exit" or when in reaches EOF.
//
// Supported commands:
//
//	reset [json]      POST /reset (optional JSON body, e.g. {"seed": 1})
//	step <json>       POST /step with the given action JSON, e.g. {"message":"hi"}
//	state             GET /state
//	help              Show command help
//	quit | exit       Leave the session
func runOpenEnvRepl(ctx context.Context, client *openEnvClient, in io.Reader, out io.Writer) error {
	printReplHelp(out)
	fmt.Fprintln(out)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Fprint(out, "rle> ")
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Fprint(out, "rle> ")
			continue
		}

		command, rest := splitCommand(line)
		switch strings.ToLower(command) {
		case "quit", "exit", "q":
			return nil
		case "help", "?":
			printReplHelp(out)
		case "reset":
			body, err := parseOptionalJSON(rest)
			if err != nil {
				fmt.Fprintf(out, "error: invalid JSON: %v\n", err)
				break
			}
			obs, err := client.reset(ctx, body)
			printReplResult(out, obs, err)
		case "step":
			if strings.TrimSpace(rest) == "" {
				fmt.Fprintln(out, "error: step requires an action JSON, e.g. step {\"message\": \"hello\"}")
				break
			}
			action, err := parseJSONObject(rest)
			if err != nil {
				fmt.Fprintf(out, "error: invalid JSON: %v\n", err)
				break
			}
			obs, err := client.step(ctx, action)
			printReplResult(out, obs, err)
		case "state":
			st, err := client.state(ctx)
			printReplResult(out, st, err)
		default:
			fmt.Fprintf(out, "error: unknown command %q (type 'help')\n", command)
		}

		fmt.Fprint(out, "rle> ")
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	// EOF (e.g. piped input or Ctrl-D): print a trailing newline for a clean prompt.
	fmt.Fprintln(out)
	return nil
}

func splitCommand(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.TrimSpace(parts[1])
}

func parseOptionalJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return parseJSONObject(raw)
}

func parseJSONObject(raw string) (map[string]any, error) {
	result := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func printReplResult(out io.Writer, payload map[string]any, err error) {
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return
	}
	data, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		fmt.Fprintf(out, "error: %v\n", marshalErr)
		return
	}
	fmt.Fprintln(out, string(data))
}

func printReplHelp(out io.Writer) {
	fmt.Fprintln(out, "Interactive OpenEnv session. Commands:")
	fmt.Fprintln(out, "  reset [json]    Reset the environment (optional body, e.g. reset {\"seed\": 1})")
	fmt.Fprintln(out, "  step <json>     Send an action (e.g. step {\"message\": \"hello\"})")
	fmt.Fprintln(out, "  state           Show current environment state")
	fmt.Fprintln(out, "  help            Show this help")
	fmt.Fprintln(out, "  quit            Exit the session")
}
