#!/usr/bin/env bash

set -euo pipefail

require_prevent_destroy() {
  local resource_type="$1"
  local resource_name="$2"
  local source_file="$3"

  if perl -0777 -e '
    my ($resource_type, $resource_name, $source_file) = @ARGV;
    open my $handle, "<", $source_file or die "cannot open $source_file: $!\n";
    my $source = <$handle>;
    $source =~ s{/\*.*?\*/}{}gs;
    $source =~ s{//[^\n]*}{}g;
    $source =~ s{#[^\n]*}{}g;

    my $type = quotemeta($resource_type);
    my $name = quotemeta($resource_name);
    my $header = qr/resource\s+"$type"\s+"$name"\s*\{/;
    exit 1 unless $source =~ /$header/g;

    my $start = pos($source);
    my $depth = 1;
    my $in_string = 0;
    my $escaped = 0;
    my $end;

    for (my $index = $start; $index < length($source); $index++) {
      my $character = substr($source, $index, 1);

      if ($in_string) {
        if ($escaped) {
          $escaped = 0;
        } elsif ($character eq "\\") {
          $escaped = 1;
        } elsif ($character eq "\"") {
          $in_string = 0;
        }
        next;
      }

      if ($character eq "\"") {
        $in_string = 1;
      } elsif ($character eq "{") {
        $depth++;
      } elsif ($character eq "}") {
        $depth--;
        if ($depth == 0) {
          $end = $index;
          last;
        }
      }
    }

    exit 1 unless defined $end;
    my $resource = substr($source, $start, $end - $start);
    my $guard = qr/lifecycle\s*\{\s*prevent_destroy\s*=\s*true\s*\}/s;
    exit($resource =~ $guard ? 0 : 1);
  ' -- "$resource_type" "$resource_name" "$source_file"; then
    return
  fi

  printf 'ERROR: production destruction guard is missing for %s.%s in %s.\n' \
    "$resource_type" "$resource_name" "$source_file" >&2
  printf '%s\n' \
    'Removing lifecycle.prevent_destroy allows Terraform to delete production infrastructure. Restore the literal guard before planning or applying.' >&2
  return 1
}

require_prevent_destroy \
  cloudflare_r2_bucket \
  this \
  modules/r2-bucket/main.tf

require_prevent_destroy \
  cloudflare_r2_custom_domain \
  packages \
  stacks/pkgs-production/main.tf
