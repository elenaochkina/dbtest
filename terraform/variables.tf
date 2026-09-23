variable "region" {
  description = "AWS region holding the cluster, the task definitions and the database under test"
  type        = string
  default     = "us-west-2"
}

variable "project" {
  description = "Name prefix and tag applied to every resource"
  type        = string
  default     = "dbtest"
}
