# Task output reaches the worker through CloudWatch, so retention only needs to
# outlive a run.
resource "aws_cloudwatch_log_group" "tasks" {
  name              = "/ecs/${var.project}"
  retention_in_days = 7
}
