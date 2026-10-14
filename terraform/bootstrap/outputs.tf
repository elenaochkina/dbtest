# Each user puts these in ~/.aws/config as role_arn under a profile.
output "deployer_role_arn" {
  value = aws_iam_role.deployer.arn
}

output "runner_role_arn" {
  value = aws_iam_role.runner.arn
}
