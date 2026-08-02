variable "account_id" {
  description = "Cloudflare account that owns the R2 bucket."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.account_id))
    error_message = "Cloudflare account ID must contain exactly 32 lowercase hexadecimal characters."
  }
}

variable "name" {
  description = "Globally unique R2 bucket name."
  type        = string
  nullable    = false

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.name))
    error_message = "R2 bucket name must be 3 to 63 lowercase letters, digits, or hyphens and must start and end with a letter or digit."
  }
}

variable "location" {
  description = "Best-effort R2 bucket location hint, fixed when the bucket is created."
  type        = string
  default     = null
  nullable    = true

  validation {
    condition     = var.location == null || contains(["apac", "eeur", "enam", "weur", "wnam", "oc"], var.location)
    error_message = "When set, R2 location must be one of apac, eeur, enam, weur, wnam, or oc."
  }
}

variable "jurisdiction" {
  description = "R2 data jurisdiction."
  type        = string
  default     = "default"
  nullable    = false

  validation {
    condition     = contains(["default", "eu", "fedramp"], var.jurisdiction)
    error_message = "R2 jurisdiction must be default, eu, or fedramp."
  }
}

variable "storage_class" {
  description = "Default storage class for new objects."
  type        = string
  default     = "Standard"
  nullable    = false

  validation {
    condition     = contains(["Standard", "InfrequentAccess"], var.storage_class)
    error_message = "R2 storage class must be Standard or InfrequentAccess."
  }
}
