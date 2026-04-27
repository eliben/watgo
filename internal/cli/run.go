package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/internal/printer"
	"github.com/eliben/watgo/wasmir"
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
	case "print":
		return runPrint(args[1:], stdin, stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdin, stdout, stderr)
	case "-V", "--version":
		fmt.Fprintln(stdout, versionString())
		return 0
	case "help":
		if len(args) == 1 {
			printRootUsage(stdout)
			return 0
		}
		switch args[1] {
		case "parse":
			return runParse([]string{"--help"}, stdin, stdout, stderr)
		case "print":
			return runPrint([]string{"--help"}, stdin, stdout, stderr)
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
	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outputPath := fs.String("output", "", "")
	fs.StringVar(outputPath, "o", "", "")
	watOutput := fs.Bool("wat", false, "")
	fs.BoolVar(watOutput, "t", false, "")
	fs.Usage = func() {
		printParseUsage(stderr)
	}

	positionals, err := parseSubcommandFlags(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "watgo parse: %v\n", err)
		return 2
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, "watgo parse: too many arguments")
		printParseUsage(stderr)
		return 2
	}

	inputPath := "-"
	if len(positionals) == 1 {
		inputPath = positionals[0]
	}
	src, err := readInput(inputPath, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "watgo parse: %v\n", err)
		return 1
	}

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
		out, err = formatParseOutput(m, *watOutput)
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
		if *watOutput {
			m, err := watgo.DecodeWASM(compiled)
			if err != nil {
				fmt.Fprintf(stderr, "watgo parse: %v\n", err)
				return 1
			}
			out, err = formatParseOutput(m, true)
			if err != nil {
				fmt.Fprintf(stderr, "watgo parse: %v\n", err)
				return 1
			}
		} else {
			out = compiled
		}
	}
	if err := writeOutput(*outputPath, out, stdout); err != nil {
		fmt.Fprintf(stderr, "watgo parse: %v\n", err)
		return 1
	}
	return 0
}

func formatParseOutput(m *wasmir.Module, watOutput bool) ([]byte, error) {
	if watOutput {
		return printer.PrintModule(m)
	}
	return watgo.EncodeWASM(m)
}

// runPrint implements `watgo print`.
//
// args are the subcommand arguments after the "print" token. Input is read
// either from the optional positional path or from stdin when the path is
// omitted or "-". Only binary wasm input is accepted for now.
func runPrint(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("print", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outputPath := fs.String("output", "", "")
	fs.StringVar(outputPath, "o", "", "")
	indent := intFlag{value: 2}
	indentText := stringFlag{}
	nameUnnamed := fs.Bool("name-unnamed", false, "")
	skeleton := fs.Bool("skeleton", false, "")
	fs.Var(&indent, "indent", "")
	fs.Var(&indentText, "indent-text", "")
	fs.Usage = func() {
		printPrintUsage(stderr)
	}

	positionals, err := parseSubcommandFlags(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "watgo print: %v\n", err)
		return 2
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, "watgo print: too many arguments")
		printPrintUsage(stderr)
		return 2
	}

	inputPath := "-"
	if len(positionals) == 1 {
		inputPath = positionals[0]
	}
	src, err := readInput(inputPath, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "watgo print: %v\n", err)
		return 1
	}
	if !isBinaryWasm(src) {
		fmt.Fprintln(stderr, "watgo print: only binary wasm input is supported for now")
		return 1
	}

	m, err := watgo.DecodeWASM(src)
	if err != nil {
		fmt.Fprintf(stderr, "watgo print: %v\n", err)
		return 1
	}
	printOptions := printer.DefaultOptions()
	if indent.set {
		printOptions.IndentText = strings.Repeat(" ", indent.value)
	}
	if indentText.set {
		printOptions.IndentText = indentText.value
	}
	printOptions.NameUnnamed = *nameUnnamed
	printOptions.Skeleton = *skeleton
	out, err := printer.PrintModuleWithOptions(m, printOptions)
	if err != nil {
		fmt.Fprintf(stderr, "watgo print: %v\n", err)
		return 1
	}
	if err := writeOutput(*outputPath, out, stdout); err != nil {
		fmt.Fprintf(stderr, "watgo print: %v\n", err)
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
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printValidateUsage(stderr)
	}

	positionals, err := parseSubcommandFlags(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "watgo validate: %v\n", err)
		return 2
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, "watgo validate: too many arguments")
		printValidateUsage(stderr)
		return 2
	}

	inputPath := "-"
	if len(positionals) == 1 {
		inputPath = positionals[0]
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
	_, err := watgo.ParseAndValidateWAT(src)
	return err
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
  watgo print [OPTIONS] [INPUT]
  watgo validate [INPUT]

Commands:
  parse              Parse WebAssembly text or binary input
  print              Print a WebAssembly binary as text
  validate           Validate a WebAssembly text or binary file
  help               Show help for the root command or a subcommand
  -V, --version      Print version information

Notes:
  Use "watgo help <command>" or "watgo <command> --help" for subcommand help.
`)
}

func versionString() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" {
			return "watgo " + info.Main.Version
		}
	}
	return "watgo unknown"
}

// printParseUsage prints help text for `watgo parse`.
func printParseUsage(w io.Writer) {
	fmt.Fprint(w, `Parse WebAssembly text or binary input.

Usage:
  watgo parse [OPTIONS] [INPUT]

Arguments:
  [INPUT]    Input file to process, or "-" for stdin. Text or binary WebAssembly are accepted.

Options:
  -o, --output <OUTPUT>
            Where to place output. If omitted, stdout is used.
  -t, --wat
            Output WebAssembly text instead of binary.
  -h, --help
            Print help
`)
}

// printPrintUsage prints help text for `watgo print`.
func printPrintUsage(w io.Writer) {
	fmt.Fprint(w, `Print a WebAssembly binary as text.

Usage:
  watgo print [OPTIONS] [INPUT]

Arguments:
  [INPUT]    Input file to process, or "-" for stdin

Options:
  -o, --output <OUTPUT>
            Where to place text output. If omitted, stdout is used.
      --name-unnamed
            Synthesize names for unnamed wasm items.
      --skeleton
            Elide function bodies and data/element payloads with "...".
      --indent <INDENT>
            Number of spaces used for indentation.
      --indent-text <INDENT_TEXT>
            String used for one indentation level; takes priority over --indent.
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

// parseSubcommandFlags parses flags registered in fs while collecting
// positional arguments.
//
// The standard flag package stops parsing at the first positional argument, but
// wasm-tools-style CLIs allow flags before or after the input path, e.g.
// `watgo parse input.wat -o out.wasm`. This helper keeps stdlib flag.Value
// parsing while making that interspersed-flag behavior explicit.
func parseSubcommandFlags(fs *flag.FlagSet, args []string) ([]string, error) {
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

		name := strings.TrimPrefix(arg, "-")
		name = strings.TrimPrefix(name, "-")
		value, hasValue := "", false
		displayName := arg
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			displayName = arg[:strings.IndexByte(arg, '=')]
			value = name[idx+1:]
			name = name[:idx]
			hasValue = true
		}

		if name == "h" || name == "help" {
			fs.Usage()
			return nil, flag.ErrHelp
		}

		f := fs.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("unknown flag %q", displayName)
		}

		if isBoolFlag(f.Value) {
			if !hasValue {
				value = "true"
			}
		} else if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: %s", displayName)
			}
			i++
			value = args[i]
		}

		if err := fs.Set(name, value); err != nil {
			return nil, fmt.Errorf("invalid value %q for flag %s: %w", value, displayName, err)
		}
	}
	return positional, nil
}

// boolFlag is the optional interface used by the standard flag package to
// identify boolean flags. Values implementing it can be set without an
// explicit value, so `--name-unnamed` is accepted as `--name-unnamed=true`.
type boolFlag interface {
	IsBoolFlag() bool
}

// isBoolFlag reports whether v supports flag-package boolean shorthand, where
// `--flag` is equivalent to `--flag=true`.
func isBoolFlag(v flag.Value) bool {
	bf, ok := v.(boolFlag)
	return ok && bf.IsBoolFlag()
}

// intFlag implements flag.Value for integer flags whose presence matters.
// It lets callers distinguish an omitted flag from an explicitly provided
// default value, and rejects negative values during flag parsing.
type intFlag struct {
	value int
	set   bool
}

func (f *intFlag) String() string {
	return strconv.Itoa(f.value)
}

func (f *intFlag) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("must be non-negative")
	}
	f.value = v
	f.set = true
	return nil
}

// stringFlag implements flag.Value for string flags whose presence matters.
// This preserves the distinction between an omitted string flag and an
// explicitly provided empty string.
type stringFlag struct {
	value string
	set   bool
}

func (f *stringFlag) String() string {
	return f.value
}

func (f *stringFlag) Set(s string) error {
	f.value = s
	f.set = true
	return nil
}
