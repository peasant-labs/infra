#!/usr/bin/env bash

set -euo pipefail

usage() {
  printf 'usage: %s [--root <dir>] <sha256-digest>\n' "${0##*/}" >&2
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
digest=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --root)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        usage
        exit 2
      fi
      repo_root="$2"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    -*)
      printf 'ERROR: unknown option: %s\n' "$1" >&2
      usage
      exit 2
      ;;
    *)
      if [ -n "$digest" ]; then
        printf 'ERROR: unexpected argument: %s\n' "$1" >&2
        usage
        exit 2
      fi
      digest="$1"
      shift
      ;;
  esac
done

if [ -z "$digest" ]; then
  printf 'ERROR: missing digest argument\n' >&2
  usage
  exit 2
fi

if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  printf 'ERROR: invalid digest %q (expected exact lowercase sha256:<64-hex>)\n' "$digest" >&2
  exit 2
fi

target_file="${repo_root}/modules/nixos/services/github-runner/default.nix"
if [ ! -f "$target_file" ]; then
  printf 'ERROR: target module not found: %s\n' "$target_file" >&2
  exit 1
fi

perl -e '
  use strict;
  use warnings;
  use File::Basename qw(dirname);
  use File::Temp qw(tempfile);

  my ($target_file, $new_digest) = @ARGV;

  die "ERROR: invalid digest \x27$new_digest\x27\n"
    unless $new_digest =~ /\Asha256:[0-9a-f]{64}\z/;

  open my $in, "<:raw", $target_file
    or die "ERROR: cannot open $target_file: $!\n";
  local $/;
  my $content = <$in>;
  close $in;

  die "ERROR: empty file: $target_file\n"
    unless defined $content && length($content) > 0;

  my @imageref_headers = ($content =~ /^[ \t]*imageRef\s*=\s*mkOption\s*\{/mg);
  die "ERROR: expected exactly 1 imageRef = mkOption block, found " . scalar(@imageref_headers) . "\n"
    unless @imageref_headers == 1;

  my @signer_headers = ($content =~ /^[ \t]*imageSigner\s*=\s*mkOption\s*\{/mg);
  die "ERROR: expected exactly 1 imageSigner = mkOption block, found " . scalar(@signer_headers) . "\n"
    unless @signer_headers == 1;

  my $signer_default_re = qr/^[ \t]*default\s*=\s*"https:\/\/github\.com\/peasant-labs\/infra\/\.github\/workflows\/runner-image\.yml\@refs\/heads\/main";[ \t]*$/m;
  my @signer_defaults = ($content =~ /$signer_default_re/g);
  die "ERROR: expected exactly 1 canonical imageSigner default, found " . scalar(@signer_defaults) . "\n"
    unless @signer_defaults == 1;

  my @repo_refs = ($content =~ /quay\.io\/peasant-labs\/github-runner\@/g);
  die "ERROR: expected exactly 1 quay.io/peasant-labs/github-runner\@ reference, found " . scalar(@repo_refs) . "\n"
    unless @repo_refs == 1;

  my $imageref_line_re = qr/^([ \t]*default\s*=\s*"quay\.io\/peasant-labs\/github-runner\@)(sha256:[0-9a-f]{64})(";[ \t]*)$/m;
  my @imageref_lines = ($content =~ /$imageref_line_re/g);
  die "ERROR: expected exactly 1 canonical imageRef default line, found " . (scalar(@imageref_lines) / 3) . "\n"
    unless @imageref_lines == 3;

  my $scoped_re = qr/(^[ \t]*imageRef\s*=\s*mkOption\s*\{(?:(?!^[ \t]*\};).)*?^[ \t]*default\s*=\s*"quay\.io\/peasant-labs\/github-runner\@)(sha256:[0-9a-f]{64})(";[ \t]*$)/ms;
  my @scoped_matches = ($content =~ /$scoped_re/g);
  die "ERROR: canonical imageRef default line is not uniquely inside imageRef = mkOption\n"
    unless @scoped_matches == 3;

  my $current_digest = $scoped_matches[1];
  if ($current_digest eq $new_digest) {
    print "No change: imageRef already pinned to $new_digest\n";
    exit 0;
  }

  my $updated = $content;
  my $replaced = ($updated =~ s/$scoped_re/${1}${new_digest}${3}/);
  die "ERROR: failed to replace imageRef digest\n" unless $replaced == 1;

  my @updated_signer = ($updated =~ /$signer_default_re/g);
  die "ERROR: internal check failed: imageSigner default changed\n"
    unless @updated_signer == 1;

  my $dir = dirname($target_file);
  my ($tmp_fh, $tmp_path) = tempfile(".default.nix.XXXXXX", DIR => $dir, UNLINK => 0);
  my $ok = eval {
    binmode($tmp_fh, ":raw");
    print {$tmp_fh} $updated or die "write failed: $!\n";
    close $tmp_fh or die "close failed: $!\n";
    rename $tmp_path, $target_file or die "rename failed: $!\n";
    1;
  };
  if (!$ok) {
    my $err = $@ || "unknown error";
    unlink $tmp_path;
    die "ERROR: $err";
  }

  print "Updated imageRef digest: $current_digest -> $new_digest\n";
' -- "$target_file" "$digest"
