mock_provider "cloudflare" {}

run "private_transcript_bucket_contract" {
  command = plan

  variables {
    cloudflare_account_id = "0123456789abcdef0123456789abcdef"
  }

  assert {
    condition     = module.transcript_bucket.name == "village-transcripts"
    error_message = "Village must retain the existing production bucket name."
  }

  assert {
    condition     = module.transcript_bucket.location == "wnam"
    error_message = "Village must retain the observed WNAM location hint."
  }

  assert {
    condition     = module.transcript_bucket.jurisdiction == "default"
    error_message = "Village must retain the default R2 jurisdiction."
  }

  assert {
    condition     = module.transcript_bucket.storage_class == "Standard"
    error_message = "Village transcript storage must use the Standard storage class."
  }

  assert {
    condition     = !module.transcript_bucket.r2_dev_enabled
    error_message = "Village must not expose transcript objects through an r2.dev URL."
  }

  assert {
    condition     = output.s3_endpoint == "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
    error_message = "Village must use the account-scoped R2 endpoint expected by its S3 client."
  }
}
