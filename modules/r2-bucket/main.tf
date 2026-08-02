resource "cloudflare_r2_bucket" "this" {
  account_id    = var.account_id
  name          = var.name
  location      = var.location
  jurisdiction  = var.jurisdiction
  storage_class = var.storage_class

  lifecycle {
    prevent_destroy = true
  }
}

resource "cloudflare_r2_managed_domain" "this" {
  account_id   = var.account_id
  bucket_name  = cloudflare_r2_bucket.this.name
  enabled      = false
  jurisdiction = var.jurisdiction
}
