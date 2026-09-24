# Instances are created and destroyed per run by provider/aws. Terraform owns
# only what they attach to.
resource "aws_db_subnet_group" "main" {
  name       = "${var.project}-main"
  subnet_ids = data.aws_subnets.default.ids
}

resource "aws_security_group" "rds" {
  name        = "${var.project}-rds"
  description = "Postgres access for the instance under test"
  vpc_id      = data.aws_vpc.default.id
}

# Fargate task IPs change every run, so the rule names the task security group
# rather than an address.
resource "aws_vpc_security_group_ingress_rule" "rds_from_task" {
  security_group_id            = aws_security_group.rds.id
  referenced_security_group_id = aws_security_group.task.id
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
}

# The worker runs on a laptop and connects for WaitForReady and the probe
# readiness check.
resource "aws_vpc_security_group_ingress_rule" "rds_from_dev" {
  security_group_id = aws_security_group.rds.id
  cidr_ipv4         = var.dev_cidr
  from_port         = 5432
  to_port           = 5432
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_egress_rule" "rds_all" {
  security_group_id = aws_security_group.rds.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}
