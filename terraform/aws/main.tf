provider "aws" {
  region  = var.region
  profile = "dbtest-terraform"

  default_tags {
    tags = {
      Project   = var.project
      ManagedBy = "terraform"
    }
  }
}
