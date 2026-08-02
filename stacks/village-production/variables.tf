variable "cloudflare_account_id" {
  description = "Cloudflare account that owns Village transcript storage."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_account_id))
    error_message = "Cloudflare account ID must contain exactly 32 lowercase hexadecimal characters."
  }
}
