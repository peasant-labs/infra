output "bucket_name" {
  description = "Private R2 bucket used for encrypted Village transcript objects."
  value       = module.transcript_bucket.name
}

output "s3_endpoint" {
  description = "Account-scoped R2 S3 API endpoint used with the separately configured bucket name."
  value       = "https://${var.cloudflare_account_id}.r2.cloudflarestorage.com"
}
