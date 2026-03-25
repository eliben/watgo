package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eliben/watgo"
)

// Run executes the watgo CLI with args and standard streams and returns the
// process exit code.
//
// args should not include argv[0]. stdin, stdout, and stderr are provided so
// tests and callers can supply their own streams.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootUsage(stderr)
		return 2
	}

	switch args[0] {
	case "parse":
		return runParse(args[1:], stdin, stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdin, stdout, stderr)
	case "help":
		if len(args) == 1 {
			printRootUsage(stdout)
			return 0
		}
		switch args[1] {
		case "parse":
			return runParse([]string{"--help"}, stdin, stdout, stderr)
		case "validate":
			return runValidate([]string{"--help"}, stdin, stdout, stderr)
		default:
			fmt.Fprintf(stderr, "watgo: unknown help topic %q\n\n", args[1])
			printRootUsage(stderr)
			return 2
		}
	case "-h", "--help":
		printRootUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "watgo: unknown subcommand %q\n\n", args[0])
		printRootUsage(stderr)
		return 2
	}
}

// runParse implements `watgo parse`.
//
// args are the subcommand arguments after the "parse" token. Input is read
// either from the optional positional path or from stdin when the path is
// omitted or "-". Output is written either to stdout or to the file named by
// -o/--output.
func runParse(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, err := normalizeArgs(args, map[string]bool{
		"-o":       true,
		"--output": true,
		"-h":       false,
		"--help":   false,
	})
	if err != nil {
		fmt.Fprintf(stderr, "watgo parse: %v\n", err)
		return 2
	}

	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outputPath := fs.String("output", "", "")
	fs.StringVar(outputPath, "o", "", "")
	fs.Usage = func() {
		printParseUsage(stderr)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "watgo parse: too many arguments")
		printParseUsage(stderr)
		return 2
	}

	inputPath := "-"
	if fs.NArg() == 1 {
		inputPath = fs.Arg(0)
	}
	src, err := readInput(inputPath, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "watgo parse: %v\n", err)
		return 1
	}

	// If the input is already a WASM binary, we validate it and re-emit it to
	// the output. This allows `watgo parse` to be used as a general-purpose
	// WASM validator and reformatter, in addition to compiling WAT text.
	var out []byte
	if isBinaryWasm(src) {
		m, err := watgo.DecodeWASM(src)
		if err != nil {
			fmt.Fprintf(stderr, "watgo parse: %v\n", err)
			return 1
		}
		if err := watgo.ValidateModule(m); err != nil {
			fmt.Fprintf(stderr, "watgo parse: %v\n", err)
			return 1
		}
		out, err = watgo.EncodeWASM(m)
		if err != nil {
			fmt.Fprintf(stderr, "watgo parse: %v\n", err)
			return 1
		}
	} else {
		compiled, err := parseToBinary(src)
		if err != nil {
			fmt.Fprintf(stderr, "watgo parse: %v\n", err)
			return 1
		}
		out = compiled
	}
	if err := writeOutput(*outputPath, out, stdout); err != nil {
		fmt.Fprintf(stderr, "watgo parse: %v\n", err)
		return 1
	}
	return 0
}

// runValidate implements `watgo validate`.
//
// args are the subcommand arguments after the "validate" token. The command
// accepts either a single optional input path or "-" for stdin and prints
// nothing on success.
func runValidate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, err := normalizeArgs(args, map[string]bool{
		"-h":     false,
		"--help": false,
	})
	if err != nil {
		fmt.Fprintf(stderr, "watgo validate: %v\n", err)
		return 2
	}

	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printValidateUsage(stderr)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "watgo validate: too many arguments")
		printValidateUsage(stderr)
		return 2
	}

	inputPath := "-"
	if fs.NArg() == 1 {
		inputPath = fs.Arg(0)
	}
	src, err := readInput(inputPath, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "watgo validate: %v\n", err)
		return 1
	}

	if err := validateInput(src); err != nil {
		fmt.Fprintf(stderr, "watgo validate: %v\n", err)
		return 1
	}
	_ = stdout
	return 0
}

// parseToBinary compiles WAT text src to binary WebAssembly bytes.
func parseToBinary(src []byte) ([]byte, error) {
	return watgo.CompileWATToWASM(src)
}

// validateInput validates either WAT text or WASM binary input.
//
// Binary input is detected by the standard `\0asm` header. Text input is
// parsed, lowered to IR, and then validated.
func validateInput(src []byte) error {
	if isBinaryWasm(src) {
		m, err := watgo.DecodeWASM(src)
		if err != nil {
			return err
		}
		return watgo.ValidateModule(m)
	}
	m, err := watgo.ParseWAT(src)
	if err != nil {
		return err
	}
	return watgo.ValidateModule(m)
}

// isBinaryWasm reports whether src appears to be a WASM binary by checking the
// leading magic number `\0asm`.
func isBinaryWasm(src []byte) bool {
	return len(src) >= 4 && bytes.Equal(src[:4], []byte{0x00, 0x61, 0x73, 0x6d})
}

// readInput reads CLI input from path or stdin.
//
// If path is empty or "-", stdin is consumed completely. Otherwise path is read
// from the filesystem.
func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "" || path == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// writeOutput writes CLI output to path or stdout.
//
// If path is empty or "-", data is written to stdout. Otherwise the named file
// is created or replaced with mode 0644.
func writeOutput(path string, data []byte, stdout io.Writer) error {
	if path == "" || path == "-" {
		_, err := stdout.Write(data)
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// printRootUsage prints the top-level watgo help text to w.
func printRootUsage(w io.Writer) {
	fmt.Fprint(w, `watgo

Usage:
  watgo parse [OPTIONS] [INPUT]
  watgo validate [INPUT]

Commands:
  parse      Parse WebAssembly text format and write binary output
  validate   Validate a WebAssembly text or binary file
`)
}

// printParseUsage prints help text for `watgo parse`.
func printParseUsage(w io.Writer) {
	fmt.Fprint(w, `Parse the WebAssembly text format.

Usage:
  watgo parse [OPTIONS] [INPUT]

Arguments:
  [INPUT]    Input file to process, or "-" for stdin

Options:
  -o, --output <OUTPUT>
            Where to place binary output. If omitted, stdout is used.
  -h, --help
            Print help
`)
}

// printValidateUsage prints help text for `watgo validate`.
func printValidateUsage(w io.Writer) {
	fmt.Fprint(w, `Validate a WebAssembly binary or text file.

Usage:
  watgo validate [INPUT]

Arguments:
  [INPUT]    Input file to process, or "-" for stdin

Options:
  -h, --help
            Print help
`)
}

// normalizeArgs reorders recognized flags ahead of positional arguments so the
// stdlib flag parser can accept wasm-tools-style command lines such as
// `watgo parse input.wat -o out.wasm`.
//
// knownFlags maps each accepted flag spelling to whether it consumes a
// following value.
func normalizeArgs(args []string, knownFlags map[string]bool) ([]string, error) {
	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		if strings.Contains(arg, "=") {
			name := arg[:strings.IndexByte(arg, '=')]
			if _, ok := knownFlags[name]; ok {
				flags = append(flags, arg)
				continue
			}
			return nil, fmt.Errorf("unknown flag %q", name)
		}
		needsValue, ok := knownFlags[arg]
		if !ok {
			return nil, fmt.Errorf("unknown flag %q", arg)
		}
		flags = append(flags, arg)
		if needsValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...), nil
}
