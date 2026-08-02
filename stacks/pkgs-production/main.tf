module "package_bucket" {
  source = "../../modules/r2-bucket"

  account_id    = var.cloudflare_account_id
  name          = "pkgs"
  jurisdiction  = "default"
  storage_class = "Standard"
}

resource "cloudflare_r2_custom_domain" "packages" {
  account_id   = var.cloudflare_account_id
  bucket_name  = module.package_bucket.name
  zone_id      = var.cloudflare_zone_id
  domain       = "pkgs.peasantlabs.org"
  enabled      = true
  jurisdiction = "default"
  min_tls      = "1.2"

  lifecycle {
    prevent_destroy = true
  }
}
