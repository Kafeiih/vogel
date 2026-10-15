// Command create-vogel-app creates a new standalone service repository from
// vogel's examples/boilerplate:
//
//	go run github.com/kafeiih/vogel/cmd/create-vogel-app@vX.Y.Z <name>
//
// It downloads the boilerplate of the vogel release it was built from (or
// copies it from a local checkout with --from), asks which optional
// components to keep, rewrites module and image names, and creates a local
// git repository. It never performs remote actions.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/kafeiih/vogel/internal/scaffold"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, stdinIsTerminal()))
}

// Terminal writes cannot be recovered from in a CLI, so their errors are
// deliberately ignored in these helpers.
func printf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func writeln(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }

func write(w io.Writer, args ...any) { _, _ = fmt.Fprint(w, args...) }

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

type config struct {
	name       string
	owner      string
	module     string
	dir        string
	version    string
	from       string
	with       listFlag
	without    listFlag
	yes        bool
	noGit      bool
	skipTidy   bool
	skipChecks bool
}

const usageHeader = `Usage: create-vogel-app [flags] <name>

Create ./<name>, a standalone service repository based on vogel's
examples/boilerplate, keeping only the optional components you choose.

  go run github.com/kafeiih/vogel/cmd/create-vogel-app@vX.Y.Z <name>

<name> must match ^[a-z][a-z0-9-]*$ (at most 50 characters); it names the
directory, the GitHub repository, the images and the database.

The boilerplate comes from the vogel release this command was run at
(@vX.Y.Z), downloaded from GitHub. --version picks another release; --from
copies it from a local vogel checkout instead.

When stdin is a terminal each optional component is asked as a yes/no
question. Otherwise, or with --yes, the template defaults are used, adjusted
with --with and --without.

Nothing is pushed or created remotely: the command ends by printing the
` + "`gh repo create`" + ` command to publish the repository.

Flags:
`

const usageFooter = `
Exit codes: 0 success, 1 runtime failure, 2 usage error.
`

func newFlagSet(cfg *config, output io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("create-vogel-app", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.owner, "owner", "", "GitHub `owner` (user or organization) of the new repository; asked when missing in interactive mode, required otherwise")
	fs.StringVar(&cfg.module, "module", "", "Go module `path` (default github.com/<lowercase owner>/<name>)")
	fs.StringVar(&cfg.dir, "dir", "", "target `directory` (default ./<name>); must not exist or be empty")
	fs.StringVar(&cfg.version, "version", "", "vogel release `vX.Y.Z` to scaffold from and require (default: the version this command was run at)")
	fs.StringVar(&cfg.from, "from", "", "copy the boilerplate from this local vogel checkout `dir` (its root); go.mod keeps a replace to it")
	fs.Var(&cfg.with, "with", "keep `component` (repeatable)")
	fs.Var(&cfg.without, "without", "remove `component` (repeatable)")
	fs.BoolVar(&cfg.yes, "yes", false, "do not ask; use the template defaults plus --with/--without")
	fs.BoolVar(&cfg.noGit, "no-git", false, "do not run git init and the initial commit")
	fs.BoolVar(&cfg.skipTidy, "skip-tidy", false, "do not run go mod tidy (offline use; run it yourself later)")
	fs.BoolVar(&cfg.skipChecks, "skip-checks", false, "do not check that the generated Go code is gofmt-formatted")
	fs.Usage = func() {
		write(fs.Output(), usageHeader)
		printFlags(fs)
		write(fs.Output(), usageFooter)
	}
	return fs
}

// printFlags lists the flags in the --name form users type.
func printFlags(fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		if name != "" {
			name = " " + name
		}
		printf(fs.Output(), "  --%s%s\n        %s\n", f.Name, name, usage)
	})
}

// parseArgs parses flags and the single positional name; flags may appear
// before or after the name.
func parseArgs(args []string) (*config, error) {
	cfg := &config{}
	// The flag package's own messages are discarded; run reports errors and
	// prints the help itself.
	fs := newFlagSet(cfg, io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	switch len(positional) {
	case 0:
		return nil, errors.New("missing <name>")
	case 1:
		cfg.name = positional[0]
	default:
		return nil, fmt.Errorf("expected exactly one <name>, got %q", positional)
	}
	if err := scaffold.ValidateName(cfg.name); err != nil {
		return nil, err
	}
	for _, w := range cfg.with {
		for _, wo := range cfg.without {
			if w == wo {
				return nil, fmt.Errorf("component %q is given to both --with and --without", w)
			}
		}
	}
	return cfg, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool) int {
	cfg, err := parseArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		newFlagSet(&config{}, stdout).Usage()
		return exitOK
	}
	if err != nil {
		printf(stderr, "create-vogel-app: %v\nRun 'create-vogel-app --help' for usage.\n", err)
		return exitUsage
	}
	ask := interactive && !cfg.yes
	in := bufio.NewReader(stdin)

	if cfg.version == "" && cfg.from == "" {
		v, err := scaffold.BuildVersion(debug.ReadBuildInfo)
		if err != nil {
			printf(stderr, "create-vogel-app: %v\n", err)
			return exitUsage
		}
		cfg.version = v
	}
	if cfg.owner == "" {
		if !ask {
			writeln(stderr, "create-vogel-app: --owner is required with --yes or when stdin is not a terminal")
			return exitUsage
		}
		if cfg.owner, err = askOwner(in, stdout); err != nil {
			printf(stderr, "create-vogel-app: %v\n", err)
			return exitUsage
		}
	}
	if cfg.module == "" {
		// Lowercase by convention: Go module paths are case-sensitive and the
		// proxy escapes capitals (github.com/!acme/...), while GitHub owners
		// are not. --module keeps an explicit mixed-case path as given.
		cfg.module = "github.com/" + strings.ToLower(cfg.owner) + "/" + cfg.name
	}
	if cfg.dir == "" {
		cfg.dir = "./" + cfg.name
	}

	var manifest *scaffold.Manifest
	opts := scaffold.Options{
		Name:       cfg.name,
		Owner:      cfg.owner,
		Module:     cfg.module,
		Dir:        cfg.dir,
		Version:    cfg.version,
		From:       cfg.from,
		SkipTidy:   cfg.skipTidy,
		SkipChecks: cfg.skipChecks,
		NoGit:      cfg.noGit,
		Log:        stdout,
		Select: func(m *scaffold.Manifest) (scaffold.Selection, error) {
			manifest = m
			var askFn scaffold.AskFunc
			if ask {
				askFn = func(c scaffold.Component) (bool, error) { return askYesNo(in, stdout, c) }
			}
			return scaffold.Select(m, cfg.with, cfg.without, askFn)
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := scaffold.Generate(ctx, opts)
	if err != nil {
		printf(stderr, "create-vogel-app: %v\n", err)
		var ue *scaffold.UsageError
		if errors.As(err, &ue) {
			return exitUsage
		}
		return exitFailure
	}
	printSummary(stdout, stderr, cfg, manifest, res)
	return exitOK
}

func askOwner(in *bufio.Reader, out io.Writer) (string, error) {
	for {
		write(out, "GitHub owner (user or organization): ")
		line, err := in.ReadString('\n')
		owner := strings.TrimSpace(line)
		if owner != "" {
			if verr := scaffold.ValidateOwner(owner); verr != nil {
				writeln(out, verr)
				if err != nil {
					return "", verr
				}
				continue
			}
			return owner, nil
		}
		if err != nil {
			return "", fmt.Errorf("reading the owner: %w", err)
		}
	}
}

func askYesNo(in *bufio.Reader, out io.Writer, c scaffold.Component) (bool, error) {
	prompt := c.Prompt
	if prompt == "" {
		prompt = fmt.Sprintf("Keep %s?", c.Name)
	}
	hint := "[y/N]"
	if c.Default {
		hint = "[Y/n]"
	}
	for {
		printf(out, "%s %s ", prompt, hint)
		line, err := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			if err != nil {
				return false, fmt.Errorf("reading the answer for %s: %w (pass --yes to use the defaults without asking)", c.Name, err)
			}
			return c.Default, nil
		case "y", "yes", "s", "si", "sí":
			return true, nil
		case "n", "no":
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("reading the answer for %s: %w", c.Name, err)
		}
		writeln(out, "Please answer y or n.")
	}
}

func printSummary(stdout, stderr io.Writer, cfg *config, m *scaffold.Manifest, res *scaffold.Result) {
	list := func(names []string) string {
		if len(names) == 0 {
			return "(none)"
		}
		return strings.Join(names, ", ")
	}
	printf(stdout, "\nCreated %s in %s\n", cfg.name, res.Dir)
	printf(stdout, "  module:  %s\n", res.Module)
	printf(stdout, "  vogel:   %s\n", res.Version)
	printf(stdout, "  kept:    %s\n", list(res.Kept))
	printf(stdout, "  removed: %s\n", list(res.Removed))
	for _, name := range res.Cascaded {
		printf(stdout, "  %s was removed because it requires %s\n", name, requirementsOf(m, name))
	}
	if res.LocalReplace != "" {
		printf(stderr, "warning: this project builds against the local vogel checkout %s through a replace in go.mod;\n"+
			"         docker build cannot see that path: drop it with `go mod edit -dropreplace=%s`\n"+
			"         and run `go mod tidy` before building images or publishing.\n",
			res.LocalReplace, scaffold.VogelModule)
	}
	if cfg.skipTidy {
		writeln(stdout, "  go mod tidy was skipped; run it before building.")
	}
	switch {
	case res.Committed:
		writeln(stdout, "  git:     initial commit on main")
	case res.GitNote != "":
		printf(stdout, "  git:     %s\n", res.GitNote)
	}

	printf(stdout, "\nNext steps:\n")
	printf(stdout, "  cd %s\n", cfg.dir)
	printf(stdout, "  cp deploy/env.example deploy/.env   # then fill it in\n")
	printf(stdout, "  make dev-infra\n")
	printf(stdout, "  gh repo create %s/%s --private --source=. --remote=origin --push\n", cfg.owner, cfg.name)
	printf(stdout, "\nPushing a vX.Y.Z tag publishes the images through .github/workflows/release.yaml.\n")
}

func requirementsOf(m *scaffold.Manifest, name string) string {
	if m != nil {
		for _, c := range m.Components {
			if c.Name == name {
				return strings.Join(c.Requires, ", ")
			}
		}
	}
	return "a component that was not kept"
}
