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

variable "image_tag" {
  description = "Image tag pushed to ECR; must match TAG in the Makefile"
  type        = string
  default     = "dev"
}

variable "dev_cidr" {
  description = "Public IP of the machine running the worker, as a /32. Changes when the ISP reassigns it."
  type        = string
}
