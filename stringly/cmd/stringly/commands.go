package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/config"
	"github.com/peasant-labs/infra/stringly/internal/failure"
	"github.com/peasant-labs/infra/stringly/internal/ratchet"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// flags are the options a command accepts. Each command registers only its
// own.
type flags struct {
	config  string
	lang    string
	format  string
	astGrep string
}

// parseFlags parses args for one command. allowPositional is how many
// positional arguments may precede the flags (show's topic is consumed
// before). It returns exit 2 output on a bad flag.
func (p process) parseFlags(cmd string, args []string, register func(*flag.FlagSet, *flags)) (flags, bool) {
	f := flags{config: config.DefaultPath}
	fs := flag.NewFlagSet("stringly "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	register(fs, &f)
	if err := fs.Parse(args); err != nil {
		environmentFailure(p.stderr,
			fmt.Sprintf("`stringly %s`: %v.", cmd, err),
			fmt.Sprintf("`stringly %s` accepts only the flags its usage lists.", cmd),
			"command-line parsing, before any file was read.",
			usageFix())
		return f, false
	}
	if fs.NArg() > 0 {
		environmentFailure(p.stderr,
			fmt.Sprintf("unexpected argument %q after `stringly %s`.", fs.Arg(0), cmd),
			fmt.Sprintf("`stringly %s` takes flags only.", cmd),
			"command-line parsing, before any file was read.",
			usageFix())
		return f, false
	}
	return f, true
}

func configFlag(fs *flag.FlagSet, f *flags) {
	fs.StringVar(&f.config, "config", config.DefaultPath, "the repository's config file")
}

func langFlag(fs *flag.FlagSet, f *flags) {
	fs.StringVar(&f.lang, "lang", "", "restrict the scan and the comparison to one language: "+joinNames(rules.AllLangs, " or "))
}

func astGrepFlag(fs *flag.FlagSet, f *flags) {
	fs.StringVar(&f.astGrep, "ast-grep", "", "the ast-grep executable (default: ast-grep on PATH)")
}

// open loads the config and asserts the ast-grep version. It prints the
// failure and returns false when either stops the run.
func (p process) open(f flags) (*session, bool) {
	cfg, err := config.Load(f.config)
	if err != nil {
		failErr(p.stderr, err, "reading "+f.config+", before any scan.")
		return nil, false
	}
	dir, err := filepath.Abs(filepath.Dir(f.config))
	if err != nil {
		failErr(p.stderr, err, "resolving the directory of "+f.config+".")
		return nil, false
	}
	runner := astgrep.NewBinary(f.astGrep)
	runner.Env = p.environ
	if err := astgrep.CheckVersion(p.ctx, runner); err != nil {
		failErr(p.stderr, err, "version check, before any scan. No file was scanned.")
		return nil, false
	}
	return &session{ctx: p.ctx, cfg: cfg, cfgPath: f.config, dir: dir, runner: runner, deps: p.deps, environ: p.environ}, true
}

// parseLang reads --lang. It prints the failure and returns false on an
// unknown language.
func (p process) parseLang(cmd Command, s string) (rules.Lang, bool) {
	if s == "" {
		return "", true
	}
	l, err := rules.ParseLang(s)
	if err != nil {
		environmentFailure(p.stderr,
			fmt.Sprintf("`stringly %s --lang %s`: %v.", cmd, s, err),
			"every rule belongs to exactly one language, and --lang selects the rules of one of them.",
			"command-line parsing, before any file was read.",
			"pass --lang "+joinNames(rules.AllLangs, " or --lang ")+", or leave it out to run every language.")
		return "", false
	}
	return l, true
}

// readBaseline reads the baseline. A missing file is reported by the caller.
func (s *session) readBaseline() (ratchet.Baseline, bool, error) {
	data, err := os.ReadFile(s.baselineFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ratchet.Baseline{}, false, nil
		}
		return ratchet.Baseline{}, false, fmt.Errorf("read the baseline %s: %w", s.cfg.Baseline, err)
	}
	b, err := ratchet.ParseBaseline(s.cfg.Baseline, data)
	return b, true, err
}

func (s *session) baselineFile() string {
	return filepath.Join(s.dir, filepath.FromSlash(s.cfg.Baseline))
}

func (s *session) options(cmd ratchet.Command, langs []rules.Lang, langFlag bool, v toolVersion) ratchet.Options {
	return ratchet.Options{
		Command:      cmd,
		BaselinePath: s.cfg.Baseline,
		Langs:        langs,
		LangFlag:     langFlag,
		Running:      ratchet.Versions{Tool: v.String(), AstGrep: astgrep.RequiredVersion},
	}
}

// noBaseline is the failure of check and update when there is no baseline.
func (p process) noBaseline(s *session, cmd Command) int {
	return environmentFailure(p.stderr,
		fmt.Sprintf("there is no baseline at %s.", s.cfg.Baseline),
		fmt.Sprintf("`stringly %s` compares the tree with the committed baseline, and there is none to compare with.", cmd),
		fmt.Sprintf("stringly %s, reading %s, after the version check. Nothing was scanned or written.", cmd, s.cfg.Baseline),
		"if this repository adopts stringly for the first time, run `stringly init` and commit the baseline it writes; "+
			"otherwise restore the baseline from the default branch.")
}

func (p process) runCheck(args []string) int {
	f, ok := p.parseFlags(CommandCheck.String(), args, func(fs *flag.FlagSet, f *flags) {
		configFlag(fs, f)
		langFlag(fs, f)
		astGrepFlag(fs, f)
		fs.StringVar(&f.format, "format", ratchet.FormatText.String(), "report format: "+joinNames(ratchet.AllFormats, " or "))
	})
	if !ok {
		return exitEnvironment
	}
	format, err := ratchet.ParseFormat(f.format)
	if err != nil {
		return environmentFailure(p.stderr, err.Error()+".",
			"check prints a text report, or GitHub Actions annotations with --format "+ratchet.FormatGitHub.String()+".",
			"command-line parsing, before any file was read.",
			"pass --format "+joinNames(ratchet.AllFormats, " or --format ")+".")
	}
	lang, ok := p.parseLang(CommandCheck, f.lang)
	if !ok {
		return exitEnvironment
	}
	s, ok := p.open(f)
	if !ok {
		return exitEnvironment
	}
	base, exists, err := s.readBaseline()
	if err != nil {
		return failErr(p.stderr, err, "reading "+s.cfg.Baseline+", before the comparison.")
	}
	if !exists {
		return p.noBaseline(s, CommandCheck)
	}
	langs := s.languages(lang)
	sc, err := s.runScan(langs)
	if err != nil {
		return failErr(p.stderr, err, "scan, before the comparison.")
	}
	out, err := ratchet.Check(sc.scan, base, s.options(ratchet.CommandCheck, langs, lang != "", p.version))
	if err != nil {
		return failErr(p.stderr, err, "stringly check, placing allow markers.")
	}
	ratchet.WriteReport(p.stdout, format, ratchet.CommandCheck, out.Findings)
	return out.Exit
}

// refuseDevBuild stops init and update in a build that names no release:
// the header they write must say which tool verified the counts.
func (p process) refuseDevBuild(cmd Command) bool {
	if !p.version.Dev {
		return false
	}
	environmentFailure(p.stderr,
		fmt.Sprintf("`stringly %s` refuses to run in a development build.", cmd),
		"the baseline header records the tool version that verified the counts, and a development build names no release.",
		"version check, before any file was read. Nothing was written.",
		"run the version the repository pins (go run github.com/peasant-labs/infra/stringly/cmd/stringly@vX.Y.Z), "+
			"or build with -ldflags \"-X main.version=vX.Y.Z\".")
	return true
}

func (p process) runUpdate(args []string) int {
	if p.refuseDevBuild(CommandUpdate) {
		return exitEnvironment
	}
	f, ok := p.parseFlags(CommandUpdate.String(), args, func(fs *flag.FlagSet, f *flags) {
		configFlag(fs, f)
		langFlag(fs, f)
		astGrepFlag(fs, f)
	})
	if !ok {
		return exitEnvironment
	}
	lang, ok := p.parseLang(CommandUpdate, f.lang)
	if !ok {
		return exitEnvironment
	}
	s, ok := p.open(f)
	if !ok {
		return exitEnvironment
	}
	base, exists, err := s.readBaseline()
	if err != nil {
		return failErr(p.stderr, err, "reading "+s.cfg.Baseline+", before the comparison.")
	}
	if !exists {
		return p.noBaseline(s, CommandUpdate)
	}
	langs := s.languages(lang)
	sc, err := s.runScan(langs)
	if err != nil {
		return failErr(p.stderr, err, "scan, before the comparison.")
	}
	git, err := ratchet.ReadGitState(p.ctx, s.dir, s.cfg.Baseline)
	if err != nil {
		return environmentFailure(p.stderr,
			fmt.Sprintf("git could not report the renames since the baseline last changed: %v.", err),
			"update moves a baseline entry only on a rename git confirms.",
			"stringly update, rename detection, after the scan. Nothing was written.",
			"run update from inside the repository's git work tree with git on PATH.")
	}
	out, err := ratchet.Update(sc.scan, base, git, s.options(ratchet.CommandUpdate, langs, lang != "", p.version))
	if err != nil {
		return failErr(p.stderr, err, "stringly update, placing allow markers. Nothing was written.")
	}
	if out.Baseline != nil {
		if err := writeFileAtomic(s.baselineFile(), out.Baseline); err != nil {
			return environmentFailure(p.stderr,
				fmt.Sprintf("the baseline %s could not be written: %v.", s.cfg.Baseline, err),
				"update computed the new baseline but the file system refused it.",
				"stringly update, writing the baseline.",
				"check the permissions of the baseline file and its directory.")
		}
		fmt.Fprintf(p.stdout, "stringly update: wrote %s.\n", s.cfg.Baseline)
		return exitClean
	}
	ratchet.WriteReport(p.stdout, ratchet.FormatText, ratchet.CommandUpdate, out.Findings)
	fmt.Fprintf(p.stdout, "stringly update: %s is unchanged.\n", s.cfg.Baseline)
	return out.Exit
}

func (p process) runInit(args []string) int {
	if p.refuseDevBuild(CommandInit) {
		return exitEnvironment
	}
	f, ok := p.parseFlags(CommandInit.String(), args, func(fs *flag.FlagSet, f *flags) {
		configFlag(fs, f)
		astGrepFlag(fs, f)
	})
	if !ok {
		return exitEnvironment
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		return failErr(p.stderr, err, "reading "+f.config+", before any scan.")
	}
	baselineFile := filepath.Join(filepath.Dir(f.config), filepath.FromSlash(cfg.Baseline))
	if _, err := os.Lstat(baselineFile); err == nil {
		fmt.Fprintf(p.stdout, "stringly init: a baseline already exists at %s.\n", cfg.Baseline)
		failure.WritePart(p.stdout, "why", "init records counts that were never lower before, which is allowed only at first adoption; "+
			"once a baseline exists, no command raises a count, whatever its header says.")
		failure.WritePart(p.stdout, "where", "stringly init, before any scan. Nothing was changed.")
		failure.WritePart(p.stdout, "fix", "run `stringly update` (the repository's update command) to lower counts and rewrite the header; "+
			"fix or mark any new hit it lists first.")
		return exitFindings
	}
	s, ok := p.open(f)
	if !ok {
		return exitEnvironment
	}
	langs := s.languages("")
	sc, err := s.runScan(langs)
	if err != nil {
		return failErr(p.stderr, err, "scan, before the baseline was written.")
	}
	out, err := ratchet.Init(sc.scan, s.options(ratchet.CommandInit, langs, false, p.version))
	if err != nil {
		return failErr(p.stderr, err, "stringly init, placing allow markers. Nothing was written.")
	}
	if out.Baseline == nil {
		ratchet.WriteReport(p.stdout, ratchet.FormatText, ratchet.CommandInit, out.Findings)
		fmt.Fprintf(p.stdout, "stringly init: nothing was written.\n")
		return out.Exit
	}
	if err := writeFileAtomic(s.baselineFile(), out.Baseline); err != nil {
		return environmentFailure(p.stderr,
			fmt.Sprintf("the baseline %s could not be written: %v.", s.cfg.Baseline, err),
			"init computed the first baseline but the file system refused it.",
			"stringly init, writing the baseline.",
			"check the permissions of the config's directory.")
	}
	fmt.Fprintf(p.stdout, "stringly init: wrote %s. Commit it with the config.\n", s.cfg.Baseline)
	return exitClean
}

// writeFileAtomic writes data to a temporary file next to path and renames
// it over path, so an interrupted write never leaves a half baseline.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stringly-baseline-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
