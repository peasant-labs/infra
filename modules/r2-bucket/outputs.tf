output "account_id" {
  description = "Cloudflare account that owns the bucket."
  value       = cloudflare_r2_bucket.this.account_id
}

output "name" {
  description = "R2 bucket name."
  value       = cloudflare_r2_bucket.this.name
}

output "location" {
  description = "R2 bucket location hint."
  value       = cloudflare_r2_bucket.this.location
}

output "jurisdiction" {
  description = "R2 bucket jurisdiction."
  value       = cloudflare_r2_bucket.this.jurisdiction
}

output "storage_class" {
  description = "Default storage class for new objects."
  value       = cloudflare_r2_bucket.this.storage_class
}

output "r2_dev_enabled" {
  description = "Whether Cloudflare's public r2.dev URL is enabled."
  value       = cloudflare_r2_managed_domain.this.enabled
}
