# The default VPC and its public subnets. Fargate tasks need egress to reach ECR
# and CloudWatch; RDS instances attach to the same subnets.
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}
