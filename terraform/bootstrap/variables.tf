variable "region" {
  description = "Region for the provider; IAM itself is global"
  type        = string
  default     = "us-west-2"
}

variable "trusted_principals" {
  description = "ARNs of the users or roles allowed to assume the dbtest roles"
  type        = list(string)

  validation {
    condition     = length(var.trusted_principals) > 0
    error_message = "List at least one principal ARN."
  }
}
