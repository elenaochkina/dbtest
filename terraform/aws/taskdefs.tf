locals {
  log_options = {
    "awslogs-group"  = aws_cloudwatch_log_group.tasks.name
    "awslogs-region" = var.region
  }
}

# stopTimeout gives the prober room to print its result before SIGKILL. The
# container name is matched by the RunTask override, so it stays fixed.
resource "aws_ecs_task_definition" "probe" {
  family                   = "${var.project}-probe"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([{
    name        = "probe"
    image       = "${aws_ecr_repository.probe.repository_url}:${var.image_tag}"
    essential   = true
    stopTimeout = 30

    logConfiguration = {
      logDriver = "awslogs"
      options   = merge(local.log_options, { "awslogs-stream-prefix" = "probe" })
    }
  }])
}

# Bench exits on its own once seeding finishes, so it needs no stopTimeout.
resource "aws_ecs_task_definition" "bench" {
  family                   = "${var.project}-bench"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 1024
  memory                   = 2048
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([{
    name      = "bench"
    image     = "${aws_ecr_repository.bench.repository_url}:${var.image_tag}"
    essential = true

    logConfiguration = {
      logDriver = "awslogs"
      options   = merge(local.log_options, { "awslogs-stream-prefix" = "bench" })
    }
  }])
}
