resource "aws_ecs_cluster" "main" {
  name = var.project
}

# Attached to every task. Nothing connects to the containers; the egress rule is
# what lets them reach ECR and CloudWatch.
resource "aws_security_group" "task" {
  name        = "${var.project}-task"
  description = "Fargate tasks running the probe and bench containers"
  vpc_id      = data.aws_vpc.default.id
}

resource "aws_vpc_security_group_egress_rule" "task_all" {
  security_group_id = aws_security_group.task.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}
