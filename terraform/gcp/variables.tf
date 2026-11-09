variable "project" {
  description = "GCP project that holds the dbtest databases"
  type        = string
}

variable "region" {
  description = "Region holding the jobs, the registry and the databases under test"
  type        = string
  default     = "us-west1"
}

variable "image_tag" {
  description = "Image tag pushed to Artifact Registry; must match TAG in the Makefile"
  type        = string
  default     = "dev"
}
