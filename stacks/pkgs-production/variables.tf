variable "cloudflare_account_id" {
  description = "Cloudflare account that owns the package origin."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_account_id))
    error_message = "Cloudflare account ID must contain exactly 32 lowercase hexadecimal characters."
  }
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone that owns peasantlabs.org."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_zone_id))
    error_message = "Cloudflare zone ID must contain exactly 32 lowercase hexadecimal characters."
  }
}
