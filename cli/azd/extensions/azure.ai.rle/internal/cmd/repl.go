// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chzyer/readline"
)

// replPrompt is the input prompt shown for each command line.
const replPrompt = "rle> "

// runOpenEnvRepl starts an interactive read-eval-print loop that drives the
// OpenEnv contract (reset/step/state) against the supplied client. It reads
// commands from in and writes results to out. The loop exits when the user
// types "quit"/"exit", presses Ctrl-D, or when in reaches EOF.
//
// When in is an interactive terminal, line editing is enabled: arrow keys
// recall and edit previous commands (history), and the usual emacs-style
// shortcuts (Ctrl-A/E/U/W, Ctrl-R reverse search) work. For piped or
// redirected input the loop falls back to a plain line scanner so scripted
// sessions behave exactly as before.
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

	if isInteractiveTerminal(in) {
		return runInteractiveRepl(ctx, client, out)
	}
	return runScannerRepl(ctx, client, in, out)
}

// runInteractiveRepl drives the loop using a line editor with history and
// in-line editing. It is used when stdin is a real terminal.
func runInteractiveRepl(ctx context.Context, client *openEnvClient, out io.Writer) error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:                 replPrompt,
		HistoryLimit:           1000,
		DisableAutoSaveHistory: false,
		AutoComplete:           replCompleter(),
		InterruptPrompt:        "^C",
		EOFPrompt:              "quit",
		Stdout:                 out,
		Stderr:                 out,
	})
	if err != nil {
		// If we cannot set up the line editor (e.g. unsupported terminal),
		// fall back to the plain scanner against stdin.
		return runScannerRepl(ctx, client, os.Stdin, out)
	}
	defer rl.Close()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		line, readErr := rl.Readline()
		if readErr != nil {
			switch {
			case errors.Is(readErr, readline.ErrInterrupt):
				// Ctrl-C clears the current line; an empty line means quit.
				if strings.TrimSpace(line) == "" {
					return nil
				}
				continue
			case errors.Is(readErr, io.EOF):
				// Ctrl-D leaves the session.
				return nil
			default:
				return readErr
			}
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if quit := dispatchReplCommand(ctx, client, line, out); quit {
			return nil
		}
	}
}

// runScannerRepl drives the loop with a simple line scanner. It is used for
// piped or redirected input where line editing is neither available nor useful.
func runScannerRepl(ctx context.Context, client *openEnvClient, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Fprint(out, replPrompt)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Fprint(out, replPrompt)
			continue
		}

		if quit := dispatchReplCommand(ctx, client, line, out); quit {
			return nil
		}

		fmt.Fprint(out, replPrompt)
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	// EOF (e.g. piped input or Ctrl-D): print a trailing newline for a clean prompt.
	fmt.Fprintln(out)
	return nil
}

// dispatchReplCommand parses and executes a single REPL command line, writing
// any result or error to out. It returns true when the session should exit.
func dispatchReplCommand(ctx context.Context, client *openEnvClient, line string, out io.Writer) bool {
	command, rest := splitCommand(line)
	switch strings.ToLower(command) {
	case "quit", "exit", "q":
		return true
	case "help", "?":
		printReplHelp(out)
	case "reset":
		body, err := parseOptionalJSON(rest)
		if err != nil {
			fmt.Fprintf(out, "error: invalid JSON: %v\n", err)
			return false
		}
		obs, err := client.reset(ctx, body)
		printReplResult(out, obs, err)
	case "step":
		if strings.TrimSpace(rest) == "" {
			fmt.Fprintln(out, "error: step requires an action JSON, e.g. step {\"message\": \"hello\"}")
			return false
		}
		action, err := parseJSONObject(rest)
		if err != nil {
			fmt.Fprintf(out, "error: invalid JSON: %v\n", err)
			return false
		}
		obs, err := client.step(ctx, action)
		printReplResult(out, obs, err)
	case "state":
		st, err := client.state(ctx)
		printReplResult(out, st, err)
	default:
		fmt.Fprintf(out, "error: unknown command %q (type 'help')\n", command)
	}
	return false
}

// replCompleter provides Tab-completion for the top-level command names.
func replCompleter() readline.AutoCompleter {
	return readline.NewPrefixCompleter(
		readline.PcItem("reset"),
		readline.PcItem("step"),
		readline.PcItem("state"),
		readline.PcItem("help"),
		readline.PcItem("quit"),
		readline.PcItem("exit"),
	)
}

// isInteractiveTerminal reports whether in is a terminal that supports line
// editing. Piped or redirected input returns false.
func isInteractiveTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return readline.IsTerminal(int(f.Fd()))
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
	fmt.Fprintln(out, "Tip: use Up/Down arrows for history, Tab to complete commands.")
}
