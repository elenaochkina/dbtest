# Values provider/aws reads from the environment.
output "rds_security_group_id" {
  description = "AWS_RDS_SECURITY_GROUP_IDS"
  value       = aws_security_group.rds.id
}

output "rds_subnet_group" {
  description = "AWS_RDS_SUBNET_GROUP"
  value       = aws_db_subnet_group.main.name
}

# Values harness/fargate needs for RunTask and log retrieval.
output "ecs_cluster" {
  value = aws_ecs_cluster.main.name
}

output "task_definition_probe" {
  value = aws_ecs_task_definition.probe.arn
}

output "task_definition_bench" {
  value = aws_ecs_task_definition.bench.arn
}

output "task_security_group_id" {
  value = aws_security_group.task.id
}

output "task_subnet_ids" {
  value = data.aws_subnets.default.ids
}

output "log_group" {
  value = aws_cloudwatch_log_group.tasks.name
}

# REGISTRY for config.mk.
output "ecr_registry" {
  value = dirname(aws_ecr_repository.bench.repository_url)
}

# terraform -chdir=terraform/aws output -raw worker_env > .env
output "worker_env" {
  description = "Shell exports for the worker"
  value       = <<-EOT
    export AWS_REGION=${var.region}
    export AWS_RDS_SECURITY_GROUP_IDS=${aws_security_group.rds.id}
    export AWS_RDS_SUBNET_GROUP=${aws_db_subnet_group.main.name}
  EOT
}
