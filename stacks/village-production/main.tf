module "transcript_bucket" {
  source = "../../modules/r2-bucket"

  account_id    = var.cloudflare_account_id
  name          = "village-transcripts"
  location      = "wnam"
  jurisdiction  = "default"
  storage_class = "Standard"
}
