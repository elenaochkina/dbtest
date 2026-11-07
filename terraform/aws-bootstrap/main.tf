# Applied once by an account admin. Creates the policies and roles that let
# everyone else deploy and run dbtest without admin access.
provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = "dbtest"
      ManagedBy = "terraform"
    }
  }
}
