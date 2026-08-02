mock_provider "cloudflare" {}

run "public_package_origin_contract" {
  command = plan

  variables {
    cloudflare_account_id = "0123456789abcdef0123456789abcdef"
    cloudflare_zone_id    = "abcdef0123456789abcdef0123456789"
  }

  assert {
    condition     = module.package_bucket.name == "pkgs"
    error_message = "The package origin must retain the canonical pkgs bucket name."
  }

  assert {
    condition     = !module.package_bucket.r2_dev_enabled
    error_message = "The package bucket must publish only through its canonical custom domain."
  }

  assert {
    condition     = cloudflare_r2_custom_domain.packages.domain == "pkgs.peasantlabs.org"
    error_message = "The package origin must retain its canonical public domain."
  }

  assert {
    condition     = cloudflare_r2_custom_domain.packages.enabled
    error_message = "The package custom domain must be enabled."
  }

  assert {
    condition     = cloudflare_r2_custom_domain.packages.min_tls == "1.2"
    error_message = "The package custom domain must reject TLS versions older than 1.2."
  }

  assert {
    condition     = output.public_origin == "https://pkgs.peasantlabs.org"
    error_message = "The package origin output must be the canonical HTTPS URL."
  }
}
