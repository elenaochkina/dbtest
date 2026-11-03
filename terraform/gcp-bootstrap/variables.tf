variable "project" {
  description = "GCP project that holds the dbtest databases"
  type        = string
}

variable "trusted_principals" {
  description = "Principals allowed to impersonate the dbtest service account, such as user:alex@example.com"
  type        = list(string)

  validation {
    condition     = length(var.trusted_principals) > 0
    error_message = "List at least one principal."
  }
}
