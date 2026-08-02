output "bucket_name" {
  description = "R2 bucket that stores signed package repositories."
  value       = module.package_bucket.name
}

output "public_origin" {
  description = "Canonical public package repository origin."
  value       = "https://${cloudflare_r2_custom_domain.packages.domain}"
}

output "s3_endpoint" {
  description = "Account-scoped R2 S3 API endpoint used by the separate package publisher."
  value       = "https://${var.cloudflare_account_id}.r2.cloudflarestorage.com"
}
